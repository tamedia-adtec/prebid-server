package magnitectv

import (
	"encoding/json"
	"testing"

	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/stretchr/testify/require"
)

// This file actually intends to test static/bidder-params/magnitectv.json

func TestValidParams(t *testing.T) {
	validator, err := openrtb_ext.NewBidderParamsValidator("../../static/bidder-params")
	require.NoError(t, err, "Failed to fetch the json-schemas. %v", err)

	for _, validParam := range validParams {
		err := validator.Validate(openrtb_ext.BidderMagniteCTV, json.RawMessage(validParam))
		require.NoError(t, err, "Schema rejected magnitectv params: %s", validParam)
	}
}

func TestInvalidParams(t *testing.T) {
	validator, err := openrtb_ext.NewBidderParamsValidator("../../static/bidder-params")
	require.NoError(t, err, "Failed to fetch the json-schemas. %v", err)

	for _, invalidParam := range invalidParams {
		err := validator.Validate(openrtb_ext.BidderMagniteCTV, json.RawMessage(invalidParam))
		require.Error(t, err, "Schema allowed unexpected params: %s", invalidParam)
	}
}

var validParams = []string{
	`{"seatCode":"ksa7s"}`,
	`{"seatCode":"gbach1","region":"eu-west-1"}`,
	`{"seatCode":"gbach1","region":"us-east-1"}`,
	`{"seatCode":"gbach1","region":"us-west-2"}`,
	`{"seatCode":"gbach1","region":"ap-southeast-1"}`,
	`{"seatCode":"gbach1","tagid":"ctv_preroll_de"}`,
	`{"seatCode":"gbach1","region":"eu-west-1","tagid":"ctv_midroll"}`,
}

var invalidParams = []string{
	``,
	`null`,
	`[]`,
	`{}`,
	`{"seatCode":""}`,
	`{"seatCode":123}`,
	`{"seatCode":"bad seat!"}`,
	`{"seatCode":"gbach1.evil.com/"}`,
	`{"seatCode":"gbach1","region":"eu-central-1"}`,
	`{"seatCode":"gbach1","tagid":""}`,
	`{"region":"eu-west-1"}`,
	`{"tagid":"ctv_preroll"}`,
}
