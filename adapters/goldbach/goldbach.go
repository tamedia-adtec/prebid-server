package goldbach

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"strconv"
	"strings"

	"github.com/buger/jsonparser"
	"github.com/mxmCherry/openrtb/v15/openrtb2"
	"github.com/prebid/prebid-server/config"
	"github.com/prebid/prebid-server/pbs"

	"golang.org/x/net/context/ctxhttp"

	"github.com/prebid/prebid-server/adapters"
	"github.com/prebid/prebid-server/errortypes"
	"github.com/prebid/prebid-server/metrics"
	"github.com/prebid/prebid-server/openrtb_ext"
)

const defaultPlatformID int = 3741

type GoldbachAdapter struct {
	http           *adapters.HTTPAdapter
	URI            string
	iabCategoryMap map[string]string
	hbSource       int
}

// used for cookies and such
func (a *GoldbachAdapter) Name() string {
	return "goldbach"
}

func (a *GoldbachAdapter) SkipNoCookies() bool {
	return false
}

type KeyVal struct {
	Key    string   `json:"key,omitempty"`
	Values []string `json:"value,omitempty"`
}

type goldbachAdapterOptions struct {
	IabCategories map[string]string `json:"iab_categories"`
}

type goldbachParams struct {
	LegacyPlacementId       int             `json:"placementId"`
	LegacyInvCode           string          `json:"invCode"`
	LegacyTrafficSourceCode string          `json:"trafficSourceCode"`
	PlacementId             int             `json:"placement_id"`
	InvCode                 string          `json:"inv_code"`
	Member                  string          `json:"member"`
	Keywords                []KeyVal        `json:"keywords"`
	TrafficSourceCode       string          `json:"traffic_source_code"`
	Reserve                 float64         `json:"reserve"`
	Position                string          `json:"position"`
	UsePmtRule              *bool           `json:"use_pmt_rule"`
	PrivateSizes            json.RawMessage `json:"private_sizes"`
}

type goldbachImpExtGoldbach struct {
	PlacementID       int             `json:"placement_id,omitempty"`
	Keywords          string          `json:"keywords,omitempty"`
	TrafficSourceCode string          `json:"traffic_source_code,omitempty"`
	UsePmtRule        *bool           `json:"use_pmt_rule,omitempty"`
	PrivateSizes      json.RawMessage `json:"private_sizes,omitempty"`
}

type goldbachImpExt struct {
	Goldbach goldbachImpExtGoldbach `json:"appnexus"`
}

type goldbachBidExtVideo struct {
	Duration int `json:"duration"`
}

type goldbachBidExtCreative struct {
	Video goldbachBidExtVideo `json:"video"`
}

type goldbachBidExtGoldbach struct {
	BidType       int                    `json:"bid_ad_type"`
	BrandId       int                    `json:"brand_id"`
	BrandCategory int                    `json:"brand_category_id"`
	CreativeInfo  goldbachBidExtCreative `json:"creative_info"`
	DealPriority  int                    `json:"deal_priority"`
}

type goldbachBidExt struct {
	Goldbach goldbachBidExtGoldbach `json:"goldbach"`
}

type goldbachReqExtGoldbach struct {
	IncludeBrandCategory    *bool  `json:"include_brand_category,omitempty"`
	BrandCategoryUniqueness *bool  `json:"brand_category_uniqueness,omitempty"`
	IsAMP                   int    `json:"is_amp,omitempty"`
	HeaderBiddingSource     int    `json:"hb_source,omitempty"`
	AdPodId                 string `json:"adpod_id,omitempty"`
}

// Full request extension including goldbach extension object
type goldbachReqExt struct {
	openrtb_ext.ExtRequest
	Goldbach *goldbachReqExtGoldbach `json:"goldbach,omitempty"`
}

var maxImpsPerReq = 10

func (a *GoldbachAdapter) Call(ctx context.Context, req *pbs.PBSRequest, bidder *pbs.PBSBidder) (pbs.PBSBidSlice, error) {
	supportedMediaTypes := []pbs.MediaType{pbs.MEDIA_TYPE_BANNER, pbs.MEDIA_TYPE_VIDEO}
	anReq, err := adapters.MakeOpenRTBGeneric(req, bidder, a.Name(), supportedMediaTypes)

	if err != nil {
		return nil, err
	}
	uri := a.URI
	for i, unit := range bidder.AdUnits {
		var params goldbachParams
		err := json.Unmarshal(unit.Params, &params)
		if err != nil {
			return nil, err
		}
		// Accept legacy Goldbach parameters if we don't have modern ones
		// Don't worry if both is set as validation rules should prevent, and this is temporary anyway.
		if params.PlacementId == 0 && params.LegacyPlacementId != 0 {
			params.PlacementId = params.LegacyPlacementId
		}
		if params.InvCode == "" && params.LegacyInvCode != "" {
			params.InvCode = params.LegacyInvCode
		}
		if params.TrafficSourceCode == "" && params.LegacyTrafficSourceCode != "" {
			params.TrafficSourceCode = params.LegacyTrafficSourceCode
		}

		if params.PlacementId == 0 && (params.InvCode == "" || params.Member == "") {
			return nil, &errortypes.BadInput{
				Message: "No placement or member+invcode provided",
			}
		}

		// Fixes some segfaults. Since this is legacy code, I'm not looking into it too deeply
		if len(anReq.Imp) <= i {
			break
		}
		if params.InvCode != "" {
			anReq.Imp[i].TagID = params.InvCode
			if params.Member != "" {
				// this assumes that the same member ID is used across all tags, which should be the case
				uri = appendMemberId(a.URI, params.Member)
			}

		}
		if params.Reserve > 0 {
			anReq.Imp[i].BidFloor = params.Reserve // TODO: we need to factor in currency here if non-USD
		}
		if anReq.Imp[i].Banner != nil && params.Position != "" {
			if params.Position == "above" {
				anReq.Imp[i].Banner.Pos = openrtb2.AdPositionAboveTheFold.Ptr()
			} else if params.Position == "below" {
				anReq.Imp[i].Banner.Pos = openrtb2.AdPositionBelowTheFold.Ptr()
			}
		}

		kvs := make([]string, 0, len(params.Keywords)*2)
		for _, kv := range params.Keywords {
			if len(kv.Values) == 0 {
				kvs = append(kvs, kv.Key)
			} else {
				for _, val := range kv.Values {
					kvs = append(kvs, fmt.Sprintf("%s=%s", kv.Key, val))
				}

			}
		}

		keywordStr := strings.Join(kvs, ",")

		impExt := goldbachImpExt{Goldbach: goldbachImpExtGoldbach{
			PlacementID:       params.PlacementId,
			TrafficSourceCode: params.TrafficSourceCode,
			Keywords:          keywordStr,
			UsePmtRule:        params.UsePmtRule,
			PrivateSizes:      params.PrivateSizes,
		}}
		anReq.Imp[i].Ext, err = json.Marshal(&impExt)
	}

	reqJSON, err := json.Marshal(anReq)
	if err != nil {
		return nil, err
	}

	debug := &pbs.BidderDebug{
		RequestURI: uri,
	}

	if req.IsDebug {
		debug.RequestBody = string(reqJSON)
		bidder.Debug = append(bidder.Debug, debug)
	}

	httpReq, err := http.NewRequest("POST", uri, bytes.NewBuffer(reqJSON))
	httpReq.Header.Add("Content-Type", "application/json;charset=utf-8")
	httpReq.Header.Add("Accept", "application/json")

	anResp, err := ctxhttp.Do(ctx, a.http.Client, httpReq)
	if err != nil {
		return nil, err
	}

	debug.StatusCode = anResp.StatusCode

	if anResp.StatusCode == 204 {
		return nil, nil
	}

	defer anResp.Body.Close()
	body, err := ioutil.ReadAll(anResp.Body)
	if err != nil {
		return nil, err
	}
	responseBody := string(body)

	if anResp.StatusCode == http.StatusBadRequest {
		return nil, &errortypes.BadInput{
			Message: fmt.Sprintf("HTTP status %d; body: %s", anResp.StatusCode, responseBody),
		}
	}

	if anResp.StatusCode != http.StatusOK {
		return nil, &errortypes.BadServerResponse{
			Message: fmt.Sprintf("HTTP status %d; body: %s", anResp.StatusCode, responseBody),
		}
	}

	if req.IsDebug {
		debug.ResponseBody = responseBody
	}

	var bidResp openrtb2.BidResponse
	err = json.Unmarshal(body, &bidResp)
	if err != nil {
		return nil, err
	}

	bids := make(pbs.PBSBidSlice, 0)

	for _, sb := range bidResp.SeatBid {
		for _, bid := range sb.Bid {
			bidID := bidder.LookupBidID(bid.ImpID)
			if bidID == "" {
				return nil, &errortypes.BadServerResponse{
					Message: fmt.Sprintf("Unknown ad unit code '%s'", bid.ImpID),
				}
			}

			pbid := pbs.PBSBid{
				BidID:       bidID,
				AdUnitCode:  bid.ImpID,
				BidderCode:  bidder.BidderCode,
				Price:       bid.Price,
				Adm:         bid.AdM,
				Creative_id: bid.CrID,
				Width:       bid.W,
				Height:      bid.H,
				DealId:      bid.DealID,
				NURL:        bid.NURL,
			}

			var impExt goldbachBidExt
			if err := json.Unmarshal(bid.Ext, &impExt); err == nil {
				if mediaType, err := getMediaTypeForBid(&impExt); err == nil {
					pbid.CreativeMediaType = string(mediaType)
					bids = append(bids, &pbid)
				}
			}
		}
	}

	return bids, nil
}

func (a *GoldbachAdapter) MakeRequests(request *openrtb2.BidRequest, reqInfo *adapters.ExtraRequestInfo) ([]*adapters.RequestData, []error) {
	memberIds := make(map[string]bool)
	errs := make([]error, 0, len(request.Imp))

	// Nexusapp openrtb2 endpoint expects imp.displaymanagerver to be populated, but some SDKs will put it in imp.ext.prebid instead
	var defaultDisplayManagerVer string
	if request.App != nil {
		source, err1 := jsonparser.GetString(request.App.Ext, openrtb_ext.PrebidExtKey, "source")
		version, err2 := jsonparser.GetString(request.App.Ext, openrtb_ext.PrebidExtKey, "version")
		if (err1 == nil) && (err2 == nil) {
			defaultDisplayManagerVer = fmt.Sprintf("%s-%s", source, version)
		}
	}
	var adPodId *bool

	for i := 0; i < len(request.Imp); i++ {
		memberId, impAdPodId, err := preprocess(&request.Imp[i], defaultDisplayManagerVer)
		if memberId != "" {
			memberIds[memberId] = true
		}
		if adPodId == nil {
			adPodId = &impAdPodId
		} else if *adPodId != impAdPodId {
			errs = append(errs, errors.New("generate ad pod option should be same for all pods in request"))
			return nil, errs
		}

		// If the preprocessing failed, the server won't be able to bid on this Imp. Delete it, and note the error.
		if err != nil {
			errs = append(errs, err)
			request.Imp = append(request.Imp[:i], request.Imp[i+1:]...)
			i--
		}
	}

	thisURI := a.URI

	// The Nexusapp API requires a Member ID in the URL. This means the request may fail if
	// different impressions have different member IDs.
	// Check for this condition, and log an error if it's a problem.
	if len(memberIds) > 0 {
		uniqueIds := keys(memberIds)
		memberId := uniqueIds[0]
		thisURI = appendMemberId(thisURI, memberId)

		if len(uniqueIds) > 1 {
			errs = append(errs, fmt.Errorf("All request.imp[i].ext.goldbach.member params must match. Request contained: %v", uniqueIds))
		}
	}

	// If all the requests were malformed, don't bother making a server call with no impressions.
	if len(request.Imp) == 0 {
		return nil, errs
	}

	// Add Goldbach request level extension
	var isAMP, isVIDEO int
	if reqInfo.PbsEntryPoint == metrics.ReqTypeAMP {
		isAMP = 1
	} else if reqInfo.PbsEntryPoint == metrics.ReqTypeVideo {
		isVIDEO = 1
	}

	var reqExt goldbachReqExt
	if len(request.Ext) > 0 {
		if err := json.Unmarshal(request.Ext, &reqExt); err != nil {
			errs = append(errs, err)
			return nil, errs
		}
	}
	if reqExt.Goldbach == nil {
		reqExt.Goldbach = &goldbachReqExtGoldbach{}
	}
	includeBrandCategory := reqExt.Prebid.Targeting != nil && reqExt.Prebid.Targeting.IncludeBrandCategory != nil
	if includeBrandCategory {
		reqExt.Goldbach.BrandCategoryUniqueness = &includeBrandCategory
		reqExt.Goldbach.IncludeBrandCategory = &includeBrandCategory
	}
	reqExt.Goldbach.IsAMP = isAMP
	reqExt.Goldbach.HeaderBiddingSource = a.hbSource + isVIDEO

	imps := request.Imp

	// For long form requests if adpodId feature enabled, adpod_id must be sent downstream.
	// Adpod id is a unique identifier for pod
	// All impressions in the same pod must have the same pod id in request extension
	// For this all impressions in  request should belong to the same pod
	// If impressions number per pod is more than maxImpsPerReq - divide those imps to several requests but keep pod id the same
	// If  adpodId feature disabled and impressions number per pod is more than maxImpsPerReq  - divide those imps to several requests but do not include ad pod id
	if isVIDEO == 1 && *adPodId {
		podImps := groupByPods(imps)

		requests := make([]*adapters.RequestData, 0, len(podImps))
		for _, podImps := range podImps {
			reqExt.Goldbach.AdPodId = generatePodId()

			reqs, errors := splitRequests(podImps, request, reqExt, thisURI, errs)
			requests = append(requests, reqs...)
			errs = append(errs, errors...)
		}
		return requests, errs
	}

	return splitRequests(imps, request, reqExt, thisURI, errs)
}

func generatePodId() string {
	val := rand.Int63()
	return fmt.Sprint(val)
}

func groupByPods(imps []openrtb2.Imp) map[string]([]openrtb2.Imp) {
	// find number of pods in response
	podImps := make(map[string][]openrtb2.Imp)
	for _, imp := range imps {
		pod := strings.Split(imp.ID, "_")[0]
		podImps[pod] = append(podImps[pod], imp)
	}
	return podImps
}

func marshalAndSetRequestExt(request *openrtb2.BidRequest, requestExtension goldbachReqExt, errs []error) {
	var err error
	request.Ext, err = json.Marshal(requestExtension)
	if err != nil {
		errs = append(errs, err)
	}
}

func splitRequests(imps []openrtb2.Imp, request *openrtb2.BidRequest, requestExtension goldbachReqExt, uri string, errs []error) ([]*adapters.RequestData, []error) {

	// Initial capacity for future array of requests, memory optimization.
	// Let's say there are 35 impressions and limit impressions per request equals to 10.
	// In this case we need to create 4 requests with 10, 10, 10 and 5 impressions.
	// With this formula initial capacity=(35+10-1)/10 = 4
	initialCapacity := (len(imps) + maxImpsPerReq - 1) / maxImpsPerReq
	resArr := make([]*adapters.RequestData, 0, initialCapacity)
	startInd := 0
	impsLeft := len(imps) > 0

	headers := http.Header{}
	headers.Add("Content-Type", "application/json;charset=utf-8")
	headers.Add("Accept", "application/json")

	marshalAndSetRequestExt(request, requestExtension, errs)

	for impsLeft {

		endInd := startInd + maxImpsPerReq
		if endInd >= len(imps) {
			endInd = len(imps)
			impsLeft = false
		}
		impsForReq := imps[startInd:endInd]
		request.Imp = impsForReq

		reqJSON, err := json.Marshal(request)
		fmt.Println("-------------------")
		var reqInterface openrtb2.BidRequest
		json.Unmarshal(reqJSON, &reqInterface)
		reqInterface.Device.IP = "213.55.241.112"
		reqInterface.Device.UA = "insomnia/2021.3.0"
		b, err := json.Marshal(reqInterface)
		reqJSON = b
		// fmt.Println(reqInterface.Device.IP)
		fmt.Println(string(reqJSON))
		fmt.Println("-------------------")
		if err != nil {
			errs = append(errs, err)
			return nil, errs
		}

		resArr = append(resArr, &adapters.RequestData{
			Method:  "POST",
			Uri:     uri,
			Body:    reqJSON,
			Headers: headers,
		})
		startInd = endInd
	}
	return resArr, errs
}

// get the keys from the map
func keys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

// preprocess mutates the imp to get it ready to send to goldbach.
//
// It returns the member param, if it exists, and an error if anything went wrong during the preprocessing.
func preprocess(imp *openrtb2.Imp, defaultDisplayManagerVer string) (string, bool, error) {
	var bidderExt adapters.ExtImpBidder
	if err := json.Unmarshal(imp.Ext, &bidderExt); err != nil {
		return "", false, err
	}
	var goldbachExt openrtb_ext.ExtImpAppnexus
	if err := json.Unmarshal(bidderExt.Bidder, &goldbachExt); err != nil {
		return "", false, err
	}

	// Accept legacy Goldbach parameters if we don't have modern ones
	// Don't worry if both is set as validation rules should prevent, and this is temporary anyway.
	if goldbachExt.PlacementId == 0 && goldbachExt.LegacyPlacementId != 0 {
		goldbachExt.PlacementId = goldbachExt.LegacyPlacementId
	}
	if goldbachExt.InvCode == "" && goldbachExt.LegacyInvCode != "" {
		goldbachExt.InvCode = goldbachExt.LegacyInvCode
	}
	if goldbachExt.TrafficSourceCode == "" && goldbachExt.LegacyTrafficSourceCode != "" {
		goldbachExt.TrafficSourceCode = goldbachExt.LegacyTrafficSourceCode
	}

	if goldbachExt.PlacementId == 0 && (goldbachExt.InvCode == "" || goldbachExt.Member == "") {
		return "", false, &errortypes.BadInput{
			Message: "No placement or member+invcode provided",
		}
	}

	if goldbachExt.InvCode != "" {
		imp.TagID = goldbachExt.InvCode
	}
	if imp.BidFloor <= 0 && goldbachExt.Reserve > 0 {
		imp.BidFloor = goldbachExt.Reserve // This will be broken for non-USD currency.
	}
	if imp.Banner != nil {
		bannerCopy := *imp.Banner
		if goldbachExt.Position == "above" {
			bannerCopy.Pos = openrtb2.AdPositionAboveTheFold.Ptr()
		} else if goldbachExt.Position == "below" {
			bannerCopy.Pos = openrtb2.AdPositionBelowTheFold.Ptr()
		}

		// Fixes #307
		if bannerCopy.W == nil && bannerCopy.H == nil && len(bannerCopy.Format) > 0 {
			firstFormat := bannerCopy.Format[0]
			bannerCopy.W = &(firstFormat.W)
			bannerCopy.H = &(firstFormat.H)
		}
		imp.Banner = &bannerCopy
	}

	// Populate imp.displaymanagerver if the SDK failed to do it.
	if len(imp.DisplayManagerVer) == 0 && len(defaultDisplayManagerVer) > 0 {
		imp.DisplayManagerVer = defaultDisplayManagerVer
	}

	impExt := goldbachImpExt{Goldbach: goldbachImpExtGoldbach{
		PlacementID:       goldbachExt.PlacementId,
		TrafficSourceCode: goldbachExt.TrafficSourceCode,
		Keywords:          makeKeywordStr(goldbachExt.Keywords),
		UsePmtRule:        goldbachExt.UsePmtRule,
		PrivateSizes:      goldbachExt.PrivateSizes,
	}}
	var err error
	if imp.Ext, err = json.Marshal(&impExt); err != nil {
		return goldbachExt.Member, goldbachExt.AdPodId, err
	}

	return goldbachExt.Member, goldbachExt.AdPodId, nil
}

func makeKeywordStr(keywords []*openrtb_ext.ExtImpAppnexusKeyVal) string {
	kvs := make([]string, 0, len(keywords)*2)
	for _, kv := range keywords {
		if len(kv.Values) == 0 {
			kvs = append(kvs, kv.Key)
		} else {
			for _, val := range kv.Values {
				kvs = append(kvs, fmt.Sprintf("%s=%s", kv.Key, val))
			}
		}
	}

	return strings.Join(kvs, ",")
}

func (a *GoldbachAdapter) MakeBids(internalRequest *openrtb2.BidRequest, externalRequest *adapters.RequestData, response *adapters.ResponseData) (*adapters.BidderResponse, []error) {
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if response.StatusCode == http.StatusBadRequest {
		return nil, []error{&errortypes.BadInput{
			Message: fmt.Sprintf("Unexpected status code: %d. Run with request.debug = 1 for more info", response.StatusCode),
		}}
	}

	if response.StatusCode != http.StatusOK {
		return nil, []error{fmt.Errorf("Unexpected status code: %d. Run with request.debug = 1 for more info", response.StatusCode)}
	}

	var bidResp openrtb2.BidResponse
	if err := json.Unmarshal(response.Body, &bidResp); err != nil {
		return nil, []error{err}
	}

	bidResponse := adapters.NewBidderResponseWithBidsCapacity(5)

	var errs []error
	for _, sb := range bidResp.SeatBid {
		for i := 0; i < len(sb.Bid); i++ {
			bid := sb.Bid[i]
			var bidExt goldbachBidExt
			if err := json.Unmarshal(bid.Ext, &bidExt); err != nil {
				errs = append(errs, err)
			} else {
				if bidType, err := getMediaTypeForBid(&bidExt); err == nil {
					if iabCategory, err := a.getIabCategoryForBid(&bidExt); err == nil {
						bid.Cat = []string{iabCategory}
					} else if len(bid.Cat) > 1 {
						//create empty categories array to force bid to be rejected
						bid.Cat = make([]string, 0, 0)
					}

					impVideo := &openrtb_ext.ExtBidPrebidVideo{
						Duration: bidExt.Goldbach.CreativeInfo.Video.Duration,
					}

					bidResponse.Bids = append(bidResponse.Bids, &adapters.TypedBid{
						Bid:          &bid,
						BidType:      bidType,
						BidVideo:     impVideo,
						DealPriority: bidExt.Goldbach.DealPriority,
					})
				} else {
					errs = append(errs, err)
				}
			}
		}
	}
	if bidResp.Cur != "" {
		bidResponse.Currency = bidResp.Cur
	}
	return bidResponse, errs
}

// getMediaTypeForBid determines which type of bid.
func getMediaTypeForBid(bid *goldbachBidExt) (openrtb_ext.BidType, error) {
	switch bid.Goldbach.BidType {
	case 0:
		return openrtb_ext.BidTypeBanner, nil
	case 1:
		return openrtb_ext.BidTypeVideo, nil
	case 2:
		return openrtb_ext.BidTypeAudio, nil
	case 3:
		return openrtb_ext.BidTypeNative, nil
	default:
		return "", fmt.Errorf("Unrecognized bid_ad_type in response from goldbach: %d", bid.Goldbach.BidType)
	}
}

// getIabCategoryForBid maps an goldbach brand id to an IAB category.
func (a *GoldbachAdapter) getIabCategoryForBid(bid *goldbachBidExt) (string, error) {
	brandIDString := strconv.Itoa(bid.Goldbach.BrandCategory)
	if iabCategory, ok := a.iabCategoryMap[brandIDString]; ok {
		return iabCategory, nil
	} else {
		return "", fmt.Errorf("category not in map: %s", brandIDString)
	}
}

func appendMemberId(uri string, memberId string) string {
	if strings.Contains(uri, "?") {
		return uri + "&member_id=" + memberId
	}

	return uri + "?member_id=" + memberId
}

// Builder builds a new instance of the Goldbach adapter for the given bidder with the given config.
func Builder(bidderName openrtb_ext.BidderName, config config.Adapter) (adapters.Bidder, error) {
	bidder := &GoldbachAdapter{
		URI:            config.Endpoint,
		iabCategoryMap: loadCategoryMapFromFileSystem(),
		hbSource:       resolvePlatformID(config.PlatformID),
	}
	return bidder, nil
}

// NewGoldbachLegacyAdapter builds a legacy version of the Goldbach adapter.
func NewGoldbachLegacyAdapter(httpConfig *adapters.HTTPAdapterConfig, endpoint, platformID string) *GoldbachAdapter {
	return &GoldbachAdapter{
		http:           adapters.NewHTTPAdapter(httpConfig),
		URI:            endpoint,
		iabCategoryMap: loadCategoryMapFromFileSystem(),
		hbSource:       resolvePlatformID(platformID),
	}
}

func resolvePlatformID(platformID string) int {
	if len(platformID) > 0 {
		if val, err := strconv.Atoi(platformID); err == nil {
			return val
		}
	}
	return defaultPlatformID
}

func loadCategoryMapFromFileSystem() map[string]string {
	// Load custom options for our adapter (currently just a lookup table to convert goldbach => iab categories)
	opts, err := ioutil.ReadFile("./static/adapter/goldbach/opts.json")
	if err == nil {
		var adapterOptions goldbachAdapterOptions

		if err := json.Unmarshal(opts, &adapterOptions); err == nil {
			return adapterOptions.IabCategories
		}
	}

	return nil
}
