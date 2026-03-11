// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Tetragon

package process

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/cilium/tetragon/pkg/reader/node"
)

func GetProcessID(pid uint32, ktime uint64) string {
	return base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%d:%d", node.GetNodeNameForExport(), ktime, pid))
}

func KtimeFromExecID(execID string) (uint64, error) {
	decoded, err := base64.StdEncoding.DecodeString(execID)
	if err != nil {
		return 0, fmt.Errorf("decode exec_id: %w", err)
	}
	s := string(decoded)
	lastColon := strings.LastIndex(s, ":")
	if lastColon <= 0 {
		return 0, fmt.Errorf("invalid exec_id format: %q", s)
	}
	secondLastColon := strings.LastIndex(s[:lastColon], ":")
	if secondLastColon < 0 {
		return 0, fmt.Errorf("invalid exec_id format: %q", s)
	}
	return strconv.ParseUint(s[secondLastColon+1:lastColon], 10, 64)
}
