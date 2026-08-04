package magnitectv

import (
	"net/http"
	"testing"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/adapters/adapterstest"
	"github.com/prebid/prebid-server/v4/config"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJsonSamples(t *testing.T) {
	bidder, buildErr := Builder(
		openrtb_ext.BidderMagniteCTV,
		config.Adapter{
			Endpoint: "https://{{.AccountID}}.{{.Region}}.eb.tremorhub.com/ad/rtb/pub",
		},
		config.Server{
			ExternalUrl: "http://hosturl.com",
			GvlID:       1,
			DataCenter:  "2",
		},
	)

	require.NoError(t, buildErr)

	adapterstest.RunJSONBidderTest(t, "magnitectvtest", bidder)
}

func TestBuilderRejectsInvalidTemplate(t *testing.T) {
	_, err := Builder(openrtb_ext.BidderMagniteCTV, config.Adapter{Endpoint: "{{Malformed"}, config.Server{})
	assert.Error(t, err)
}

// The waterfall tier from bid.ext.tier must surface as DealPriority so upstream
// prioritization can use it; the raw bid.ext must stay untouched for passthrough.
func TestMakeBidsMapsTierToDealPriority(t *testing.T) {
	bidder, buildErr := Builder(
		openrtb_ext.BidderMagniteCTV,
		config.Adapter{Endpoint: "https://{{.AccountID}}.{{.Region}}.eb.tremorhub.com/ad/rtb/pub"},
		config.Server{},
	)
	require.NoError(t, buildErr)

	body := []byte(`{
		"id": "req-1",
		"cur": "USD",
		"seatbid": [{
			"seat": "TremorVideo",
			"bid": [{
				"id": "bid-1",
				"impid": "1",
				"price": 12.5,
				"adm": "<VAST version=\"4.0\"></VAST>",
				"ext": {"tier": 3, "sequence": 1}
			}]
		}]
	}`)

	response, errs := bidder.MakeBids(&openrtb2.BidRequest{}, nil, &adapters.ResponseData{
		StatusCode: http.StatusOK,
		Body:       body,
	})

	require.Empty(t, errs)
	require.Len(t, response.Bids, 1)
	assert.Equal(t, 3, response.Bids[0].DealPriority)
	assert.Equal(t, openrtb_ext.BidTypeVideo, response.Bids[0].BidType)
	assert.JSONEq(t, `{"tier": 3, "sequence": 1}`, string(response.Bids[0].Bid.Ext))
}
