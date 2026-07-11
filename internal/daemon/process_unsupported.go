//go:build !linux && !darwin

package daemon

func ProcessAlive(int) bool {
	return false
}
