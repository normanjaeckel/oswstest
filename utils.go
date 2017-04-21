package main

import "github.com/OneOfOne/xxhash"

func hashData(data []byte) uint64 {
	hash := xxhash.New64()
	hash.Write(data)
	return hash.Sum64()
}
