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

package filter

import (
	"bytes"
	"strings"

	"github.com/buger/jsonparser"
	jsoniter "github.com/json-iterator/go"
	api "github.com/tigrisdata/tigris/api/server/v1"
	"github.com/tigrisdata/tigris/errors"
	"github.com/tigrisdata/tigris/query/expression"
	"github.com/tigrisdata/tigris/schema"
	ulog "github.com/tigrisdata/tigris/util/log"
	"github.com/tigrisdata/tigris/value"
)

var (
	filterNone  = []byte(`{}`)
	emptyFilter = &WrappedFilter{Filter: &EmptyFilter{}}
)

// A Filter represents a query filter that can have any multiple conditions, logical filtering, nested conditions, etc.
// On a high level, a filter from a user query will map like this
//
//	{Selector} --> Filter with a single condition
//	{Selector, Selector, LogicalOperator} --> Filter with two condition and a logicalOperator
//	{Selector, LogicalOperator} --> Filter with single condition and a logicalOperator
//	and so on...
//
// The JSON representation for these filters will look like below,
// "filter: {"f1": 10}
// "filter": [{"f1": 10}, {"f2": {"$gt": 10}}]
// "filter": [{"f1": 10}, {"f2": 10}, {"$or": [{"f3": 20}, {"$and": [{"f4":5}, {"f5": 6}]}]}]
//
// The default rule applied between filters are "$and and the default selector is "$eq".
type Filter interface {
	// Matches returns true if the input doc passes the filter, otherwise false
	Matches(doc []byte, metadata []byte) bool
	// MatchesDoc similar to Matches but used when document is already parsed
	MatchesDoc(doc map[string]any) bool
	ToSearchFilter() string
	// IsSearchIndexed to let caller knows if there is any fields in the query not indexed in search. This
	// will trigger full scan.
	IsSearchIndexed() bool
}

type EmptyFilter struct{}

func (*EmptyFilter) Matches(_ []byte, _ []byte) bool  { return true }
func (*EmptyFilter) MatchesDoc(_ map[string]any) bool { return true }
func (*EmptyFilter) ToSearchFilter() string           { return "" }
func (*EmptyFilter) IsSearchIndexed() bool            { return false }

type WrappedFilter struct {
	Filter

	searchFilter string
}

func NewWrappedFilter(filters []Filter) *WrappedFilter {
	if len(filters) == 0 {
		return &WrappedFilter{
			Filter:       emptyFilter,
			searchFilter: emptyFilter.ToSearchFilter(),
		}
	} else if len(filters) <= 1 {
		return &WrappedFilter{
			Filter:       filters[0],
			searchFilter: filters[0].ToSearchFilter(),
		}
	}

	andF := &AndFilter{
		filter: filters,
	}

	return &WrappedFilter{
		Filter:       andF,
		searchFilter: andF.ToSearchFilter(),
	}
}

func (w *WrappedFilter) None() bool {
	return w.Filter == emptyFilter
}

func (w *WrappedFilter) SearchFilter() string {
	return w.searchFilter
}

func (w *WrappedFilter) IsSearchIndexed() bool {
	return w.Filter.IsSearchIndexed()
}

func None(reqFilter []byte) bool {
	return len(reqFilter) == 0 || bytes.Equal(reqFilter, filterNone)
}

type Factory struct {
	fields    []*schema.QueryableField
	collation *value.Collation
	// For secondary Indexes do the following:
	// 1. Reject Case insensitive queries
	// 2. Always use Factory Top level collation because it will be a sort key collation
	buildForSecondaryIndex bool
}

func NewFactory(fields []*schema.QueryableField, collation *value.Collation) *Factory {
	return &Factory{
		fields:                 fields,
		collation:              collation,
		buildForSecondaryIndex: false,
	}
}

func NewFactoryForSecondaryIndex(fields []*schema.QueryableField) *Factory {
	return &Factory{
		fields:                 fields,
		collation:              value.NewSortKeyCollation(),
		buildForSecondaryIndex: true,
	}
}

func (factory *Factory) WrappedFilter(reqFilter []byte) (*WrappedFilter, error) {
	filters, err := factory.Factorize(reqFilter)
	if err != nil {
		return nil, err
	}

	return NewWrappedFilter(filters), nil
}

func (factory *Factory) Factorize(reqFilter []byte) ([]Filter, error) {
	if len(reqFilter) == 0 {
		return nil, nil
	}

	var filters []Filter
	var err error
	err = jsonparser.ObjectEach(reqFilter, func(k []byte, v []byte, jsonDataType jsonparser.ValueType, offset int) error {
		if err != nil {
			return err
		}

		var filter Filter
		switch string(k) {
		case string(AndOP):
			filter, err = factory.UnmarshalAnd(v)
		case string(OrOP):
			filter, err = factory.UnmarshalOr(v)
		default:
			filter, err = factory.ParseSelector(k, v, jsonDataType)
		}
		if err != nil {
			return err
		}
		filters = append(filters, filter)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return filters, nil
}

func (factory *Factory) UnmarshalFilter(input jsoniter.RawMessage) (expression.Expr, error) {
	var err error
	var filter Filter
	parsingError := jsonparser.ObjectEach(input, func(k []byte, v []byte, dt jsonparser.ValueType, offset int) error {
		if err != nil {
			return err
		}

		switch string(k) {
		case string(AndOP):
			filter, err = factory.UnmarshalAnd(v)
		case string(OrOP):
			filter, err = factory.UnmarshalOr(v)
		default:
			filter, err = factory.ParseSelector(k, v, dt)
		}
		return nil
	})

	if parsingError != nil {
		return filter, parsingError
	}

	return filter, err
}

func (factory *Factory) UnmarshalAnd(input jsoniter.RawMessage) (Filter, error) {
	expr, err := expression.UnmarshalArray(input, factory.UnmarshalFilter)
	if err != nil {
		return nil, err
	}
	andFilters, err := convertExprListToFilters(expr)
	if err != nil {
		return nil, err
	}

	return NewAndFilter(andFilters)
}

func (factory *Factory) UnmarshalOr(input jsoniter.RawMessage) (Filter, error) {
	expr, err := expression.UnmarshalArray(input, factory.UnmarshalFilter)
	if err != nil {
		return nil, err
	}
	orFilters, err := convertExprListToFilters(expr)
	if err != nil {
		return nil, err
	}

	return NewOrFilter(orFilters)
}

func convertExprListToFilters(expr []expression.Expr) ([]Filter, error) {
	filters := make([]Filter, 0, len(expr))
	for _, e := range expr {
		f, ok := e.(Filter)
		if !ok {
			return nil, ulog.CE("not able to decode to filter %v", f)
		}
		filters = append(filters, f)
	}

	return filters, nil
}

func (factory *Factory) filterToQueryableField(filterField string) (*schema.QueryableField, *schema.QueryableField) {
	var (
		field *schema.QueryableField
		// parent is needed in case of an array where we need to extract first the array from the document.
		parent *schema.QueryableField
	)
	for _, f := range factory.fields {
		if f.Name() == filterField {
			field = f
			break
		}

		for _, nested := range f.AllowedNestedQFields {
			if nested.Name() == filterField {
				field = nested
				parent = f
				break
			}
		}
	}

	return field, parent
}

// ParseSelector is a short-circuit for Selector i.e. when we know the filter passed is not logical then we directly
// call this because if it is not logical then it is simply a Selector filter.
func (factory *Factory) ParseSelector(k []byte, v []byte, dataType jsonparser.ValueType) (Filter, error) {
	filterField := string(k)
	field, parent := factory.filterToQueryableField(filterField)
	if field == nil {
		// try level - 1
		idx := strings.LastIndex(filterField, ".")
		if idx <= 0 {
			return nil, errors.InvalidArgument("querying on non schema field '%s'", string(k))
		}

		if field, parent = factory.filterToQueryableField(filterField[0:idx]); field == nil && parent == nil {
			return nil, errors.InvalidArgument("querying on non schema field '%s'", string(k))
		}

		parent = field
		field = schema.NewDynamicQueryableField(filterField, filterField[idx+1:], schema.UnknownType)
	}

	if field == nil {
		return nil, errors.InvalidArgument("querying on non schema field '%s'", string(k))
	}

	switch dataType {
	case jsonparser.Boolean, jsonparser.Number, jsonparser.String, jsonparser.Array, jsonparser.Null:
		tigrisType := toTigrisType(field, dataType)

		if dataType == jsonparser.Null {
			// need to explicitly set as nil otherwise, jsonparser is setting it as []byte{null}
			v = nil
		}

		var val value.Value
		var err error
		if factory.collation != nil {
			val, err = value.NewValueUsingCollation(tigrisType, v, factory.collation)
		} else {
			val, err = value.NewValue(tigrisType, v)
		}
		if err != nil {
			return nil, err
		}

		return NewSelector(parent, field, NewEqualityMatcher(val), factory.collation), nil
	case jsonparser.Object:
		valueMatcher, likeMatcher, collation, err := buildValueMatcher(v, field, factory.collation, factory.buildForSecondaryIndex)
		if err != nil {
			return nil, err
		}
		if likeMatcher != nil {
			return NewLikeFilter(field, likeMatcher), nil
		}

		if collation != nil {
			return NewSelector(parent, field, valueMatcher, collation), nil
		}
		return NewSelector(parent, field, valueMatcher, factory.collation), nil
	default:
		return nil, errors.InvalidArgument("unable to parse the comparison operator")
	}
}

// buildValueMatcher is a helper method to create a value matcher object when the value of a Selector is an object
// instead of a simple JSON value. Apart from comparison operators, this object can have its own collation, which
// needs to be honored at the field level. Therefore, the caller needs to check if the collation returned by the
// method is not nil and if yes, use this collation..
func buildValueMatcher(input jsoniter.RawMessage, field *schema.QueryableField, factoryCollation *value.Collation, buildForSecondaryIndex bool) (ValueMatcher, LikeMatcher, *value.Collation, error) {
	if len(input) == 0 {
		return nil, nil, nil, errors.InvalidArgument("empty object")
	}

	var (
		valueMatcher ValueMatcher
		LikeMatcher  LikeMatcher
		collation    *value.Collation
		err          error
	)
	if collation, err = buildCollation(input, factoryCollation, buildForSecondaryIndex); err != nil {
		return nil, nil, nil, err
	}

	err = jsonparser.ObjectEach(input, func(key []byte, v []byte, dataType jsonparser.ValueType, offset int) error {
		if err != nil {
			return err
		}

		switch string(key) {
		case EQ, GT, GTE, LT, LTE:
			switch dataType {
			case jsonparser.Boolean, jsonparser.Number, jsonparser.String, jsonparser.Null, jsonparser.Array:
				tigrisType := toTigrisType(field, dataType)

				var val value.Value
				//nolint:gocritic
				if buildForSecondaryIndex {
					val, err = value.NewValueUsingCollation(tigrisType, v, factoryCollation)
				} else if collation != nil {
					val, err = value.NewValueUsingCollation(tigrisType, v, collation)
				} else {
					val, err = value.NewValue(tigrisType, v)
				}
				if err != nil {
					return err
				}

				valueMatcher, err = NewMatcher(string(key), val)
				return err
			}
		case REGEX, CONTAINS, NOT:
			if dataType != jsonparser.String {
				return errors.InvalidArgument("string is only supported type for 'regex/contains/not' filters")
			}
			if !(field.DataType == schema.StringType || (field.DataType == schema.ArrayType && field.SubType == schema.StringType)) {
				return errors.InvalidArgument("field '%s' of type '%s' is not supported for 'regex/contains/not' filters. Only 'string' or an 'array of string' is supported", field.FieldName, schema.FieldNames[field.DataType])
			}

			LikeMatcher, err = NewLikeMatcher(string(key), string(v), collation)
			return err
		case api.CollationKey:
		default:
			return errors.InvalidArgument("expression is not supported inside comparison operator %s", string(key))
		}
		return nil
	})

	return valueMatcher, LikeMatcher, collation, err
}

func buildCollation(input jsoniter.RawMessage, factoryCollation *value.Collation, buildForSecondaryIndex bool) (*value.Collation, error) {
	c, dt, _, _ := jsonparser.Get(input, api.CollationKey)
	if dt == jsonparser.NotExist {
		return factoryCollation, nil
	}

	var (
		err          error
		apiCollation *api.Collation
	)
	// this will override the default collation
	if err = jsoniter.Unmarshal(c, &apiCollation); err != nil {
		return nil, err
	}
	if err = apiCollation.IsValid(); err != nil {
		return nil, err
	}

	collation := value.NewCollationFrom(apiCollation)
	if buildForSecondaryIndex && collation.IsCaseInsensitive() {
		return nil, errors.InvalidArgument("found case insensitive collation")
	}

	return collation, nil
}

func toTigrisType(field *schema.QueryableField, jsonType jsonparser.ValueType) schema.FieldType {
	switch field.DataType {
	case schema.ArrayType:
		if jsonType != jsonparser.Array && !(field.SubType == schema.ArrayType || field.SubType == schema.ObjectType) {
			return field.SubType
		}
		return jsonToTigrisType(jsonType)
	case schema.UnknownType:
		field.DataType = jsonToTigrisType(jsonType)
	}

	return field.DataType
}

func jsonToTigrisType(jsonType jsonparser.ValueType) schema.FieldType {
	switch jsonType {
	case jsonparser.Boolean:
		return schema.BoolType
	case jsonparser.String:
		return schema.StringType
	case jsonparser.Number:
		return schema.DoubleType
	case jsonparser.Array:
		return schema.ArrayType
	case jsonparser.Null:
		return schema.NullType
	}

	return schema.UnknownType
}
