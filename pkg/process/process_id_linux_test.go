// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Tetragon

package process

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKtimeFromExecID_Valid(t *testing.T) {
	// Format: base64("nodename:ktime:pid")
	raw := "mynode:123456789:42"
	execID := base64.StdEncoding.EncodeToString([]byte(raw))

	ktime, err := KtimeFromExecID(execID)
	require.NoError(t, err)
	assert.Equal(t, uint64(123456789), ktime)
}

func TestKtimeFromExecID_NodeNameWithColons(t *testing.T) {
	// Nodename containing colons (e.g. IPv6 address).
	raw := "node:with:colons:987654321:100"
	execID := base64.StdEncoding.EncodeToString([]byte(raw))

	ktime, err := KtimeFromExecID(execID)
	require.NoError(t, err)
	assert.Equal(t, uint64(987654321), ktime)
}

func TestKtimeFromExecID_RoundTrip(t *testing.T) {
	// Use GetProcessID and verify KtimeFromExecID extracts the same ktime.
	var expectedKtime uint64 = 5555555555
	var pid uint32 = 1234

	execID := GetProcessID(pid, expectedKtime)

	ktime, err := KtimeFromExecID(execID)
	require.NoError(t, err)
	assert.Equal(t, expectedKtime, ktime)
}

func TestKtimeFromExecID_InvalidBase64(t *testing.T) {
	_, err := KtimeFromExecID("not-valid-base64!!!")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode exec_id")
}

func TestKtimeFromExecID_MissingColons(t *testing.T) {
	execID := base64.StdEncoding.EncodeToString([]byte("nocolons"))
	_, err := KtimeFromExecID(execID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid exec_id format")
}

func TestKtimeFromExecID_SingleColon(t *testing.T) {
	execID := base64.StdEncoding.EncodeToString([]byte("one:colon"))
	_, err := KtimeFromExecID(execID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid exec_id format")
}

func TestKtimeFromExecID_NonNumericKtime(t *testing.T) {
	raw := "node:notanumber:42"
	execID := base64.StdEncoding.EncodeToString([]byte(raw))

	_, err := KtimeFromExecID(execID)
	assert.Error(t, err)
}

func TestKtimeFromExecID_LargeKtime(t *testing.T) {
	// Test with max-ish uint64 ktime value.
	var largeKtime uint64 = 18446744073709551615
	raw := fmt.Sprintf("node:%d:1", largeKtime)
	execID := base64.StdEncoding.EncodeToString([]byte(raw))

	ktime, err := KtimeFromExecID(execID)
	require.NoError(t, err)
	assert.Equal(t, largeKtime, ktime)
}
