/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package utils

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPrintHelpers(t *testing.T) {
	// Map helper (single element to avoid nondeterministic order)
	m := map[string]int{"a": 1}
	require.Equal(t, "\"a=1\"", PrintMapAndQuote(m))

	// Slice helper
	s := []int{1, 2}
	got := PrintSliceAndQuoteWithControl(s, func(v int, _ int) string { return fmt.Sprint(v) })
	require.Equal(t, "\"1,2\"", got)

	// Map with control
	m2 := map[string]int{"b": 2}
	got = PrintMapAndQuoteWithControl(m2, func(k string, v int) string {
		return fmt.Sprintf("%s=>%d", k, v)
	})
	require.Equal(t, "\"b=>2\"", got)

	// Duration helper
	dur := map[string]v1.Duration{"wait": {Duration: 5 * time.Second}}
	got = PrintMapAndQuoteDuration(dur)
	require.True(t, strings.HasPrefix(got, "\"wait="))
	require.True(t, strings.HasSuffix(got, "s\""))

	// Single quote map with control
	m3 := map[string]int{"c": 3}
	got = PrintMapAndQuoteWithSingleQuote(m3, func(k string, v int) string {
		return fmt.Sprintf("%s=>%d", k, v)
	})
	require.Equal(t, "'c=>3'", got)

	// Eviction helper
	got = PrintMapAndQuoteForEviction(map[string]int{"evict-after": 10})
	require.Equal(t, "'evict-after<10'", got)

	// Duration eviction helper
	got = PrintMapAndQuoteDurationForEviction(dur)
	require.True(t, strings.HasPrefix(got, "'wait="))
	require.True(t, strings.HasSuffix(got, "s'"))

	// pretty JSON
	j := PrettyPrintAsJson("json:%s", map[string]string{"k": "v"})
	require.True(t, strings.HasPrefix(j, "json:{"))
	require.Contains(t, j, "\"k\": \"v\"")
}
