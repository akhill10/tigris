// Copyright 2022-2023 Tigris Data, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"testing"

	jsoniter "github.com/json-iterator/go"
	"github.com/stretchr/testify/require"
)

func TestJSONEncoding(t *testing.T) {
	// ToDo: add marshaler tests
	inputDoc := []byte(`{"pkey_int": 1, "int_value": 2, "str_value": "foo"}`)
	b, err := jsoniter.Marshal(inputDoc)
	require.NoError(t, err)

	var bb []byte
	require.NoError(t, jsoniter.Unmarshal(b, &bb))

	t.Run("unmarshal SearchRequest", func(t *testing.T) {
		inputDoc := []byte(`{"q":"my search text","search_fields":["first_name","last_name"],
							"filter":{"last_name":"Steve"},"facet":{"facet stat":0},
							"sort":[{"salary":"$asc"}],"include_fields":["employment","history"]}`)

		req := &SearchRequest{}
		err := jsoniter.Unmarshal(inputDoc, req)
		require.NoError(t, err)
		require.Equal(t, "my search text", req.GetQ())
		require.Equal(t, []string{"first_name", "last_name"}, req.GetSearchFields())
		require.Equal(t, []byte(`{"last_name":"Steve"}`), req.GetFilter())
		require.Equal(t, []byte(`{"facet stat":0}`), req.GetFacet())
		require.Equal(t, []byte(`[{"salary":"$asc"}]`), req.GetSort())
		require.Equal(t, []string{"employment", "history"}, req.GetIncludeFields())
	})

	t.Run("marshal SearchResponse", func(t *testing.T) {
		avg := float64(40)
		resp := &SearchResponse{
			Hits: nil,
			Facets: map[string]*SearchFacet{
				"myField": {
					Counts: []*FacetCount{{
						Count: 32,
						Value: "adidas",
					}},
					Stats: &FacetStats{
						Count: 50,
						Avg:   &avg,
					},
				},
			},
			Meta: &SearchMetadata{
				TotalPages: 0,
				Found:      1234,
				Page: &Page{
					Current: 2,
					Size:    10,
				},
			},
		}
		r, err := jsoniter.Marshal(resp)
		require.NoError(t, err)
		require.JSONEq(t, `{"hits":[],"facets":{"myField":{"counts":[{"count":32,"value":"adidas"}],"stats":{"avg":40,"count":50}}},"meta":{"found":1234, "matched_fields":null, "total_pages":0,"page":{"current":2,"size":10}}}`, string(r))
	})
}

func TestQueryMetricsRequest(t *testing.T) {
	exp := `{
    "db": "db1",
    "branch": "br1",
    "collection": "coll1",
    "from": 123,
    "to": 234,
    "metric_name": "metric1",
    "quantile": 0.99,
    "tigris_operation": "ALL",
    "space_aggregation": "SUM",
    "space_aggregated_by": [ "field1", "field2" ],
    "function": "RATE",
    "additional_functions": [{
            "rollup": {
				"aggregator": "SUM",
                "interval": 12
            }
        }]
	}`

	var r1 QueryTimeSeriesMetricsRequest
	err := jsoniter.Unmarshal([]byte(exp), &r1)
	require.NoError(t, err)

	expReq := QueryTimeSeriesMetricsRequest{
		Db:                "db1",
		Branch:            "br1",
		Collection:        "coll1",
		From:              123,
		To:                234,
		MetricName:        "metric1",
		TigrisOperation:   TigrisOperation_ALL,
		SpaceAggregation:  MetricQuerySpaceAggregation_SUM,
		SpaceAggregatedBy: []string{"field1", "field2"},
		Function:          MetricQueryFunction_RATE,
		Quantile:          0.99,
		AdditionalFunctions: []*AdditionalFunction{
			{
				Rollup: &RollupFunction{
					Aggregator: RollupAggregator_ROLLUP_AGGREGATOR_SUM,
					Interval:   12,
				},
			},
		},
	}

	require.Equal(t, &expReq, &r1)
}
