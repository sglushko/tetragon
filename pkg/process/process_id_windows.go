// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Tetragon

package process

import (
	"encoding/base64"
	"fmt"
)

func GetProcessID(pid uint32, _ uint64) string {
	return base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%d:%d:%d", pid, pid, pid))
}

// KtimeFromExecID is not supported on Windows because the Windows exec_id
// format does not include ktime.
func KtimeFromExecID(_ string) (uint64, error) {
	return 0, nil
}
