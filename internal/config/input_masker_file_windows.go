package config

import "os"

func setInputMaskerFileBlocking(_ *os.File) error {
	// Windows does not use O_NONBLOCK for os.OpenFile (the flag is ignored).
	return nil
}
