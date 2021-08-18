package doboz

import (
	"encoding/binary"
)

type Compressor struct {
	dict Dictionary
}

// Returns the maximum compressed size of any block of data with the specified size
// This function should be used to determine the size of the compression destination buffer
func GetMaxCompressedSize(size int) int {
	// The header + the original uncompressed data
	return getHeaderSize(MaxInt) + size
}

func getHeaderSize(maxCompressedSize int) int {
	return 1 + 2*getSizeCodedSize(maxCompressedSize)
}

func getSizeCodedSize(size int) int {
	if size <= 255 {
		return 1
	}

	if size <= 65535 {
		return 2
	}

	/*if (size <= MaxUint) {
	    return 4
	}

	return 8*/

	return 4
}

// Compresses a block of data
// The source and destination buffers must not overlap and their size must be greater than 0
// This operation is memory safe
// On success, returns RESULT_OK and outputs the compressed size
func (c *Compressor) Compress(source []byte, destination []byte, compressedSize *int) Result {
	if len(source) == 0 {
		return RESULT_ERROR_BUFFER_TOO_SMALL
	}

	maxCompressedSize := GetMaxCompressedSize(len(source))
	if len(destination) < maxCompressedSize {
		return RESULT_ERROR_BUFFER_TOO_SMALL
	}

	inputBuffer := source
	outputBuffer := destination

	// Compute the maximum output end pointer
	// We use this to determine whether we should store the data instead of compressing it
	maxOutputEnd := maxCompressedSize
	// Allocate the header
	outputIterator := getHeaderSize(maxCompressedSize)

	// Initialize the dictionary
	c.dict.SetBuffer(inputBuffer)

	// Initialize the control word which contains the literal/match bits
	// The highest bit of a control word is a guard bit, which marks the end of the bit list
	// The guard bit simplifies and speeds up the decoding process, and it
	const controlWordBitCount int = WORD_SIZE*8 - 1
	const controlWordGuardBit uint = uint(1) << controlWordBitCount
	controlWord := controlWordGuardBit
	controlWordBit := 0

	// Since we do not know the contents of the control words in advance, we allocate space for them and subsequently fill them with data as soon as we can
	// This is necessary because the decoder must encounter a control word *before* the literals and matches it refers to
	// We begin the compressed data with a control word
	controlWordPointer := outputIterator
	outputIterator += WORD_SIZE

	// The match located at the current inputIterator position
	var match Match

	// The match located at the next inputIterator position
	// Initialize it to 'no match', because we are at the beginning of the inputIterator buffer
	// A match with a length of 0 means that there is no match
	var nextMatch Match
	nextMatch.Length = 0

	// The dictionary matching look-ahead is 1 character, so set the dictionary position to 1
	// We don't have to worry about getting matches beyond the inputIterator, because the dictionary ignores such requests
	c.dict.Skip()

	// At each position, we select the best match to encode from a list of match candidates provided by the match finder
	var matchCandidates [MAX_MATCH_CANDIDATE_COUNT]Match
	var matchCandidateCount int

	// Iterate while there is still data left
	for c.dict.Position()-1 < len(source) {
		// Check whether the output is too large
		// During each iteration, we may output up to 8 bytes (2 words), and the compressed stream ends with 4 dummy bytes
		if outputIterator+2*WORD_SIZE+TRAILING_DUMMY_SIZE > maxOutputEnd {
			// Stop the compression and instead store
			return c.store(source, destination, compressedSize)
		}

		// Check whether the control word must be flushed
		if controlWordBit == controlWordBitCount {
			// Flush current control word
			FastWrite(outputBuffer[controlWordPointer:], controlWord, WORD_SIZE)

			// New control word
			controlWord = controlWordGuardBit
			controlWordBit = 0

			controlWordPointer = outputIterator
			outputIterator += WORD_SIZE
		}

		// The current match is the previous 'next' match
		match = nextMatch

		// Find the best match at the next position
		// The dictionary position is automatically incremented
		matchCandidateCount = c.dict.FindMatches(matchCandidates[:])
		nextMatch = c.getBestMatch(matchCandidates[:matchCandidateCount])

		// If we have a match, do not immediately use it, because we may miss an even better match (lazy evaluation)
		// If encoding a literal and the next match has a higher compression ratio than encoding the current match, discard the current match
		if match.Length > 0 && (1+nextMatch.Length)*c.getMatchCodedSize(match) > match.Length*(1+c.getMatchCodedSize(nextMatch)) {
			match.Length = 0
		}

		// Check whether we must encode a literal or a match
		if match.Length == 0 {
			// Encode a literal (0 control word flag)
			// In order to efficiently decode literals in runs, the literal bit (0) must differ from the guard bit (1)

			// The current dictionary position is now two characters ahead of the literal to encode
			FastWrite(outputBuffer[outputIterator:], uint(inputBuffer[c.dict.Position()-2]), 1)
			outputIterator++
		} else {
			// Encode a match (1 control word flag)
			controlWord |= uint(1 << controlWordBit)

			outputIterator += c.encodeMatch(match, outputBuffer[outputIterator:])

			// Skip the matched characters
			for i := 0; i < match.Length-2; i++ {
				c.dict.Skip()
			}

			matchCandidateCount = c.dict.FindMatches(matchCandidates[:])
			nextMatch = c.getBestMatch(matchCandidates[:matchCandidateCount])
		}

		// Next control word bit
		controlWordBit++
	}

	// Flush the control word
	FastWrite(outputBuffer[controlWordPointer:], controlWord, WORD_SIZE)

	// Output trailing safety dummy bytes
	// This reduces the number of necessary buffer checks during decoding
	FastWrite(outputBuffer[outputIterator:], 0, TRAILING_DUMMY_SIZE)
	outputIterator += TRAILING_DUMMY_SIZE

	// Done, compute the compressed size
	*compressedSize = outputIterator

	// Encode the header
	var header Header
	header.Version = VERSION
	header.IsStored = false
	header.UncompressedSize = uint64(len(source))
	header.CompressedSize = uint64(*compressedSize)

	c.encodeHeader(header, maxCompressedSize, outputBuffer)

	// Return the compressed size
	return RESULT_OK
}

// Store the source
func (c *Compressor) store(source []byte, destination []byte, compressedSize *int) Result {
	outputBuffer := destination
	outputIterator := 0

	// Encode the header
	maxCompressedSize := GetMaxCompressedSize(len(source))
	headerSize := getHeaderSize(maxCompressedSize)

	*compressedSize = headerSize + len(source)

	var header Header
	header.Version = VERSION
	header.IsStored = true
	header.UncompressedSize = uint64(len(source))
	header.CompressedSize = uint64(*compressedSize)

	c.encodeHeader(header, maxCompressedSize, destination)
	outputIterator += headerSize

	// Store the data
	copy(outputBuffer[outputIterator:], source)

	return RESULT_OK
}

func (c *Compressor) getBestMatch(matchCandidates []Match) (bestMatch Match) {
	bestMatch.Length = 0

	// Select the longest match which can be coded efficiently (coded size is less than the length)
	for _, matchCandidate := range matchCandidates {
		if matchCandidate.Length > c.getMatchCodedSize(matchCandidate) {
			bestMatch = matchCandidate
			break
		}
	}

	return
}

func (c *Compressor) encodeMatch(match Match, destination []byte) int {
	var word uint
	var size int

	lengthCode := uint(match.Length - MIN_MATCH_LENGTH)
	offsetCode := uint(match.Offset)

	if lengthCode == 0 && offsetCode < 64 {
		word = offsetCode << 2 // 00
		size = 1
	} else if lengthCode == 0 && offsetCode < 16384 {
		word = (offsetCode << 2) | 1 // 01
		size = 2
	} else if lengthCode < 16 && offsetCode < 1024 {
		word = (offsetCode << 6) | (lengthCode << 2) | 2 // 10
		size = 2
	} else if lengthCode < 32 && offsetCode < 65536 {
		word = (offsetCode << 8) | (lengthCode << 3) | 3 // 11
		size = 3
	} else {
		word = (offsetCode << 11) | (lengthCode << 3) | 7 // 111
		size = 4
	}

	if destination != nil {
		FastWrite(destination, word, size)
	}

	return size
}

func (c *Compressor) getMatchCodedSize(match Match) int {
	return c.encodeMatch(match, nil)
}

func (c *Compressor) encodeHeader(header Header, maxCompressedSize int, destination []byte) {
	// Encode the attribute byte
	attributes := uint(header.Version)

	sizeCodedSize := uint(getSizeCodedSize(maxCompressedSize))
	attributes |= (sizeCodedSize - 1) << 3

	if header.IsStored {
		attributes |= 128
	}

	destination[0] = byte(attributes)
	destination = destination[1:]

	// Encode the uncompressed and compressed sizes
	switch sizeCodedSize {
	case 1:
		destination[0] = byte(header.UncompressedSize)
		destination[sizeCodedSize] = byte(header.CompressedSize)

	case 2:
		binary.LittleEndian.PutUint16(destination, uint16(header.UncompressedSize))
		binary.LittleEndian.PutUint16(destination[2:], uint16(header.CompressedSize))

	case 4:
		binary.LittleEndian.PutUint32(destination, uint32(header.UncompressedSize))
		binary.LittleEndian.PutUint32(destination[4:], uint32(header.CompressedSize))

	case 8:
		binary.LittleEndian.PutUint64(destination, header.UncompressedSize)
		binary.LittleEndian.PutUint64(destination[8:], header.CompressedSize)
	}
}
