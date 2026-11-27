//go:build windows

package runner

import "golang.org/x/sys/windows/registry"

func windowsManagedClaudePolicy() bool {
	for _, hive := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, arch := range []uint32{0, registry.WOW64_32KEY, registry.WOW64_64KEY} {
			key, err := registry.OpenKey(hive, `SOFTWARE\Policies\ClaudeCode`, registry.QUERY_VALUE|arch)
			if err == nil {
				_ = key.Close()
				return true
			}
			if err != registry.ErrNotExist {
				return true
			}
		}
	}
	return false
}
