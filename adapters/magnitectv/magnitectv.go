package magnitectv

import (
	"encoding/json"
	"fmt"
	"net/http"
	"text/template"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/config"
	"github.com/prebid/prebid-server/v4/errortypes"
	"github.com/prebid/prebid-server/v4/macros"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/prebid/prebid-server/v4/util/jsonutil"
)

// Adapter for the Magnite CTV (SpringServe) "Publisher OpenRTB Connect" integration.
// Spec: "Magnite CTV Publisher OpenRTB Connect" — OpenRTB 2.5, per-seat regional endpoint,
// GDPR/consent in 2.5 ext locations, schain at source.schain, ad pods via imp.video.ext.
const defaultRegion = "eu-west-1"

var supportedRegions = map[string]struct{}{
	"us-east-1":      {},
	"us-west-2":      {},
	"ap-southeast-1": {},
	"eu-west-1":      {},
}

type adapter struct {
	endpointTemplate *template.Template
}

// endpointKey groups imps that can be sent in the same outbound request.
type endpointKey struct {
	seatCode string
	region   string
}

type bidExt struct {
	Tier int `json:"tier"`
}

func Builder(bidderName openrtb_ext.BidderName, config config.Adapter, server config.Server) (adapters.Bidder, error) {
	tmpl, err := template.New("endpointTemplate").Parse(config.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("unable to parse endpoint template: %v", err)
	}
	return &adapter{endpointTemplate: tmpl}, nil
}

func (a *adapter) MakeRequests(request *openrtb2.BidRequest, reqInfo *adapters.ExtraRequestInfo) ([]*adapters.RequestData, []error) {
	var errs []error

	// group imps by seat code + region so each group maps to one endpoint
	groupedImps := make(map[endpointKey][]openrtb2.Imp)
	var groupOrder []endpointKey

	for _, imp := range request.Imp {
		key, impCopy, err := buildImp(imp)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, seen := groupedImps[key]; !seen {
			groupOrder = append(groupOrder, key)
		}
		groupedImps[key] = append(groupedImps[key], impCopy)
	}

	if len(groupedImps) == 0 {
		errs = append(errs, &errortypes.BadInput{Message: "no valid impression found"})
		return nil, errs
	}

	headers := http.Header{}
	headers.Add("Content-Type", "application/json")
	headers.Add("Accept", "application/json")
	headers.Add("x-openrtb-version", "2.5")

	var reqs []*adapters.RequestData
	for _, key := range groupOrder {
		outRequest, err := buildRequest(*request, groupedImps[key])
		if err != nil {
			errs = append(errs, err)
			continue
		}

		uri, err := macros.ResolveMacros(a.endpointTemplate, macros.EndpointTemplateParams{
			SeatID: key.seatCode,
			Region: key.region,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("unable to resolve endpoint: %v", err))
			continue
		}

		body, err := jsonutil.Marshal(outRequest)
		if err != nil {
			errs = append(errs, &errortypes.FailedToMarshal{Message: fmt.Errorf("unable to marshal request: %w", err).Error()})
			continue
		}

		reqs = append(reqs, &adapters.RequestData{
			Method:  "POST",
			Uri:     uri,
			Body:    body,
			Headers: headers,
			ImpIDs:  openrtb_ext.GetImpIDs(outRequest.Imp),
		})
	}

	return reqs, errs
}

func (a *adapter) MakeBids(bidReq *openrtb2.BidRequest, unused *adapters.RequestData, httpRes *adapters.ResponseData) (*adapters.BidderResponse, []error) {
	if httpRes.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if httpRes.StatusCode == http.StatusBadRequest {
		return nil, []error{&errortypes.BadInput{
			Message: fmt.Sprintf("unexpected status code: %d. Run with request.debug = 1 for more info", httpRes.StatusCode),
		}}
	}

	if httpRes.StatusCode != http.StatusOK {
		return nil, []error{&errortypes.BadServerResponse{
			Message: fmt.Sprintf("unexpected status code: %d. Run with request.debug = 1 for more info", httpRes.StatusCode),
		}}
	}

	var resp openrtb2.BidResponse
	if err := jsonutil.Unmarshal(httpRes.Body, &resp); err != nil {
		return nil, []error{&errortypes.BadServerResponse{
			Message: fmt.Errorf("unable to unmarshal response: %w", err).Error(),
		}}
	}

	bidderResponse := adapters.NewBidderResponse()
	bidderResponse.Currency = resp.Cur

	for _, sb := range resp.SeatBid {
		for i := range sb.Bid {
			typedBid := &adapters.TypedBid{
				Bid:     &sb.Bid[i],
				BidType: openrtb_ext.BidTypeVideo,
			}

			// ext.tier (1-16) is the Magnite CTV waterfall priority of the winning demand
			if len(sb.Bid[i].Ext) > 0 {
				var ext bidExt
				if err := jsonutil.Unmarshal(sb.Bid[i].Ext, &ext); err == nil {
					typedBid.DealPriority = ext.Tier
				}
			}

			bidderResponse.Bids = append(bidderResponse.Bids, typedBid)
		}
	}

	return bidderResponse, nil
}

func buildImp(imp openrtb2.Imp) (endpointKey, openrtb2.Imp, error) {
	params, err := extractImpParams(&imp)
	if err != nil {
		return endpointKey{}, openrtb2.Imp{}, err
	}

	region := params.Region
	if region == "" {
		region = defaultRegion
	}
	if _, ok := supportedRegions[region]; !ok {
		return endpointKey{}, openrtb2.Imp{}, &errortypes.BadInput{
			Message: fmt.Sprintf("unsupported region %q for imp %s", region, imp.ID),
		}
	}

	if params.TagID != "" {
		imp.TagID = params.TagID
	}

	// all params map to standard fields or the endpoint — nothing left for imp.ext
	imp.Ext = nil

	if imp.Video != nil {
		video, err := buildVideo(*imp.Video)
		if err != nil {
			return endpointKey{}, openrtb2.Imp{}, err
		}
		imp.Video = video
	}

	return endpointKey{seatCode: params.SeatCode, region: region}, imp, nil
}

func extractImpParams(imp *openrtb2.Imp) (*openrtb_ext.ImpExtMagniteCTV, error) {
	var extImpBidder adapters.ExtImpBidder
	if err := jsonutil.Unmarshal(imp.Ext, &extImpBidder); err != nil {
		return nil, &errortypes.BadInput{
			Message: fmt.Errorf("unable to unmarshal imp.ext: %w", err).Error(),
		}
	}

	var params openrtb_ext.ImpExtMagniteCTV
	if err := jsonutil.Unmarshal(extImpBidder.Bidder, &params); err != nil {
		return nil, &errortypes.BadInput{
			Message: fmt.Errorf("unable to unmarshal imp.ext.bidder: %w", err).Error(),
		}
	}

	if params.SeatCode == "" {
		return nil, &errortypes.BadInput{Message: "seatCode is required"}
	}

	return &params, nil
}

// buildVideo translates OpenRTB 2.6 ad pod fields into the Magnite CTV 2.5 extension names
// (imp.video.ext.podduration / maxseq / podsequence). Explicit values already present in
// video.ext win over the native 2.6 fields.
func buildVideo(video openrtb2.Video) (*openrtb2.Video, error) {
	if video.PodDur == 0 && video.MaxSeq == 0 && video.PodSeq == 0 {
		return &video, nil
	}

	ext := make(map[string]json.RawMessage)
	if len(video.Ext) > 0 {
		if err := jsonutil.Unmarshal(video.Ext, &ext); err != nil {
			return nil, &errortypes.BadInput{
				Message: fmt.Errorf("unable to unmarshal imp.video.ext: %w", err).Error(),
			}
		}
	}

	setIfAbsent := func(key string, value int64) {
		if _, exists := ext[key]; !exists && value != 0 {
			ext[key], _ = jsonutil.Marshal(value)
		}
	}
	setIfAbsent("podduration", video.PodDur)
	setIfAbsent("maxseq", video.MaxSeq)
	setIfAbsent("podsequence", int64(video.PodSeq))

	extJSON, err := jsonutil.Marshal(ext)
	if err != nil {
		return nil, &errortypes.FailedToMarshal{Message: fmt.Errorf("unable to marshal imp.video.ext: %w", err).Error()}
	}

	video.Ext = extJSON
	video.PodDur = 0
	video.MaxSeq = 0
	video.PodSeq = 0

	return &video, nil
}

func buildRequest(request openrtb2.BidRequest, imps []openrtb2.Imp) (*openrtb2.BidRequest, error) {
	request.Imp = imps

	if err := moveSChain(&request); err != nil {
		return nil, err
	}

	ext, err := filterRequestExt(request.Ext)
	if err != nil {
		return nil, err
	}
	request.Ext = ext

	return &request, nil
}

// moveSChain relocates the supply chain from the OpenRTB 2.5 location (source.ext.schain,
// where PBS core places it for 2.5 adapters) to source.schain, where the Magnite CTV spec
// expects it.
func moveSChain(request *openrtb2.BidRequest) error {
	if request.Source == nil || len(request.Source.Ext) == 0 {
		return nil
	}

	var sourceExt map[string]json.RawMessage
	if err := jsonutil.Unmarshal(request.Source.Ext, &sourceExt); err != nil {
		return &errortypes.BadInput{
			Message: fmt.Errorf("unable to unmarshal source.ext: %w", err).Error(),
		}
	}

	rawSChain, ok := sourceExt["schain"]
	if !ok {
		return nil
	}

	var schain openrtb2.SupplyChain
	if err := jsonutil.Unmarshal(rawSChain, &schain); err != nil {
		return &errortypes.BadInput{
			Message: fmt.Errorf("unable to unmarshal source.ext.schain: %w", err).Error(),
		}
	}

	source := *request.Source
	source.SChain = &schain

	delete(sourceExt, "schain")
	if len(sourceExt) == 0 {
		source.Ext = nil
	} else {
		extJSON, err := jsonutil.Marshal(sourceExt)
		if err != nil {
			return &errortypes.FailedToMarshal{Message: fmt.Errorf("unable to marshal source.ext: %w", err).Error()}
		}
		source.Ext = extJSON
	}

	request.Source = &source
	return nil
}

// filterRequestExt drops PBS-internal request.ext content and keeps only ext.extra,
// the Magnite CTV custom targeting/passthrough container.
func filterRequestExt(requestExt json.RawMessage) (json.RawMessage, error) {
	if len(requestExt) == 0 {
		return nil, nil
	}

	var ext map[string]json.RawMessage
	if err := jsonutil.Unmarshal(requestExt, &ext); err != nil {
		return nil, &errortypes.BadInput{
			Message: fmt.Errorf("unable to unmarshal request.ext: %w", err).Error(),
		}
	}

	extra, ok := ext["extra"]
	if !ok {
		return nil, nil
	}

	filtered, err := jsonutil.Marshal(map[string]json.RawMessage{"extra": extra})
	if err != nil {
		return nil, &errortypes.FailedToMarshal{Message: fmt.Errorf("unable to marshal request.ext: %w", err).Error()}
	}
	return filtered, nil
}
