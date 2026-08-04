package openrtb_ext

// ImpExtMagniteCTV defines the contract for bidrequest.imp[i].ext.prebid.bidder.magnitectv
type ImpExtMagniteCTV struct {
	// SeatCode is the Magnite CTV account seat code, used as the endpoint subdomain
	// (https://[SEAT_CODE].[REGION].eb.tremorhub.com/ad/rtb/pub).
	SeatCode string `json:"seatCode"`
	// Region selects the regional endpoint. Defaults to eu-west-1 (EMEA).
	Region string `json:"region,omitempty"`
	// TagID is written to imp.tagid for supply routing rules within the seat.
	TagID string `json:"tagid,omitempty"`
}
