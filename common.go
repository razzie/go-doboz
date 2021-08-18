package doboz

import "encoding/binary"

type Result int

const (
	RESULT_OK Result = iota
	RESULT_ERROR_BUFFER_TOO_SMALL
	RESULT_ERROR_CORRUPTED_DATA
	RESULT_ERROR_UNSUPPORTED_VERSION
)

type Match struct {
	Length int
	Offset int
}

type Header struct {
	UncompressedSize uint64
	CompressedSize   uint64
	Version          int
	IsStored         bool
}

const (
	VERSION = 0 // encoding format

	WORD_SIZE = 4 // uint32_t

	MIN_MATCH_LENGTH          = 3
	MAX_MATCH_LENGTH          = 255 + MIN_MATCH_LENGTH
	MAX_MATCH_CANDIDATE_COUNT = 128
	DICTIONARY_SIZE           = 1 << 21 // 2 MB, must be a power of 2!

	TAIL_LENGTH         = 2 * WORD_SIZE // prevents fast write operations from writing beyond the end of the buffer during decoding
	TRAILING_DUMMY_SIZE = WORD_SIZE     // safety trailing bytes which decrease the number of necessary buffer checks
)

// Reads up to 4 bytes and returns them in a word
// WARNING: May read more bytes than requested!
func FastRead(source []byte, sourceOffset int, size int) uint {
	switch size {
	case 4:
		return uint(binary.LittleEndian.Uint32(source[sourceOffset:]))
	case 3:
		return uint(binary.LittleEndian.Uint32(source[sourceOffset:]))
	case 2:
		return uint(binary.LittleEndian.Uint16(source[sourceOffset:]))
	case 1:
		return uint(source[sourceOffset])
	default:
		return 0
	}
}

// Writes up to 4 bytes specified in a word
// WARNING: May write more bytes than requested!
func FastWrite(destination []byte, destinationOffset int, word uint, size int) {
	switch size {
	case 4:
		binary.LittleEndian.PutUint32(destination[destinationOffset:destinationOffset+4], uint32(word))
	case 3:
		binary.LittleEndian.PutUint32(destination[destinationOffset:destinationOffset+4], uint32(word))
	case 2:
		binary.LittleEndian.PutUint16(destination[destinationOffset:destinationOffset+4], uint16(word))
	case 1:
		destination[destinationOffset] = byte(word)
	}
}

const (
	MaxUint = ^uint(0)
	MinUint = 0
	MaxInt  = int(MaxUint >> 1)
	MinInt  = -MaxInt - 1
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func Hash(data []byte, pos int) uint {
	// FNV-1a hash
	const prime uint = 16777619
	var result uint = 2166136261

	result = (result ^ uint(data[pos+0])) * prime
	result = (result ^ uint(data[pos+1])) * prime
	result = (result ^ uint(data[pos+2])) * prime

	return result
}
