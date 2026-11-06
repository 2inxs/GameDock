//go:build !windows

package world

func lockSourceWorld(string) (func(), error) {
	return func() {}, nil
}
