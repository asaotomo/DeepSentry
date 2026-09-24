//go:build !windows

package collector

func localWindowsContext() (SystemContext, bool) {
	return SystemContext{}, false
}
