//go:build !darwin

package main

func platformLockKeys() lockKeyStore { return fileLockKeys{} }
