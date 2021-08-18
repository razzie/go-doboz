package doboz

import "encoding/binary"

type CompressionInfo struct {
	UncompressedSize uint64
	CompressedSize   uint64
	Version          int
}

type LookupTable struct {
	mask        uint // the mask for the entire encoded match
	offsetShift byte
	lengthMask  byte
	lengthShift byte
	size        int8 // the size of the encoded match in bytes
}

type Decompressor struct {
	literalRunLengthTable []int8
	lut                   []LookupTable
}

func (d *Decompressor) initialize() {
	d.literalRunLengthTable = []int8{4, 0, 1, 0, 2, 0, 1, 0, 3, 0, 1, 0, 2, 0, 1, 0}
	d.lut = []LookupTable{
		{mask: 0xff, offsetShift: 2, lengthMask: 0, lengthShift: 0, size: 1},          // (0)00
		{mask: 0xffff, offsetShift: 2, lengthMask: 0, lengthShift: 0, size: 2},        // (0)01
		{mask: 0xffff, offsetShift: 6, lengthMask: 15, lengthShift: 2, size: 2},       // (0)10
		{mask: 0xffffff, offsetShift: 8, lengthMask: 31, lengthShift: 3, size: 3},     // (0)11
		{mask: 0xff, offsetShift: 2, lengthMask: 0, lengthShift: 0, size: 1},          // (1)00 = (0)00
		{mask: 0xffff, offsetShift: 2, lengthMask: 0, lengthShift: 0, size: 2},        // (1)01 = (0)01
		{mask: 0xffff, offsetShift: 6, lengthMask: 15, lengthShift: 2, size: 2},       // (1)10 = (0)10
		{mask: 0xffffffff, offsetShift: 11, lengthMask: 255, lengthShift: 3, size: 4}, // 111
	}
}

// Decompresses a block of data
// The source and destination buffers must not overlap
// This operation is memory safe
// On success, returns RESULT_OK
func (d *Decompressor) Decompress(source []byte, destination []byte) Result {
	inputBuffer := source
	inputIterator := 0

	outputBuffer := destination
	outputIterator := 0

	// Decode the header
	decodeHeaderResult, header, headerSize := d.decodeHeader(source)

	if decodeHeaderResult != RESULT_OK {
		return decodeHeaderResult
	}

	inputIterator += headerSize

	if header.Version != VERSION {
		return RESULT_ERROR_UNSUPPORTED_VERSION
	}

	// Check whether the supplied buffers are large enough
	if uint64(len(source)) < header.CompressedSize || uint64(len(destination)) < header.UncompressedSize {
		return RESULT_ERROR_BUFFER_TOO_SMALL
	}

	uncompressedSize := int(header.UncompressedSize)

	// If the data is simply stored, copy it to the destination buffer and we're done
	if header.IsStored {
		copy(outputBuffer[:uncompressedSize], inputBuffer[inputIterator:])
		return RESULT_OK
	}

	inputEnd := int(header.CompressedSize)
	outputEnd := uncompressedSize

	// Compute pointer to the first byte of the output 'tail'
	// Fast write operations can be used only before the tail, because those may write beyond the end of the output buffer
	outputTail := 0
	if uncompressedSize > TAIL_LENGTH {
		outputTail = outputEnd - TAIL_LENGTH
	}

	// Initialize the control word to 'empty'
	controlWord := uint(1)

	// Decoding loop
	for {
		// Check whether there is enough data left in the input buffer
		// In order to decode the next literal/match, we have to read up to 8 bytes (2 words)
		// Thanks to the trailing dummy, there must be at least 8 remaining input bytes
		if inputIterator+2*WORD_SIZE > inputEnd {
			return RESULT_ERROR_CORRUPTED_DATA
		}

		// Check whether we must read a control word
		if controlWord == 1 {
			controlWord = FastRead(inputBuffer[inputIterator:], WORD_SIZE)
			inputIterator += WORD_SIZE
		}

		// Detect whether it's a literal or a match
		if (controlWord & 1) == 0 {
			// It's a literal

			// If we are before the tail, we can safely use fast writing operations
			if outputIterator < outputTail {
				// We copy literals in runs of up to 4 because it's faster than copying one by one

				// Copy implicitly 4 literals regardless of the run length
				FastWrite(outputBuffer[outputIterator:], FastRead(inputBuffer[inputIterator:], WORD_SIZE), WORD_SIZE)

				// Get the run length using a lookup table
				runLength := int(d.literalRunLengthTable[controlWord&0xf])

				// Advance the inputBuffer and outputBuffer pointers with the run length
				inputIterator += runLength
				outputIterator += runLength

				// Consume as much control word bits as the run length
				controlWord >>= runLength
			} else {
				// We have reached the tail, we cannot output literals in runs anymore
				// Output all remaining literals
				for outputIterator < outputEnd {
					// Check whether there is enough data left in the input buffer
					// In order to decode the next literal, we have to read up to 5 bytes
					if inputIterator+WORD_SIZE+1 > inputEnd {
						return RESULT_ERROR_CORRUPTED_DATA
					}

					// Check whether we must read a control word
					if controlWord == 1 {
						controlWord = FastRead(inputBuffer[inputIterator:], WORD_SIZE)
						inputIterator += WORD_SIZE
					}

					// Output one literal
					// We cannot use fast read/write functions
					outputBuffer[outputIterator] = inputBuffer[inputIterator] // !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!! ++i vagy i++ ?
					outputIterator++
					inputIterator++

					// Next control word bit
					controlWord >>= 1
				}

				// Done
				return RESULT_OK
			}
		} else {
			// It's a match

			// Decode the match
			match, matchSize := d.decodeMatch(inputBuffer[inputIterator:])
			inputIterator += matchSize

			// Copy the matched string
			// In order to achieve high performance, we copy characters in groups of machine words
			// Overlapping matches require special care
			matchString := outputIterator - match.Offset

			// Check whether the match is out of range
			if matchString < 0 || outputIterator+match.Length > outputTail {
				return RESULT_ERROR_CORRUPTED_DATA
			}

			i := 0

			if match.Offset < WORD_SIZE {
				// The match offset is less than the word size
				// In order to correctly handle the overlap, we have to copy the first three bytes one by one
				for i < 3 {
					FastWrite(outputBuffer[outputIterator+i:], FastRead(outputBuffer[matchString+i:], 1), 1) // !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!! 2. input v output?
					i++
				}

				// With this trick, we increase the distance between the source and destination pointers
				// This enables us to use fast copying for the rest of the match
				matchString -= 2 + (match.Offset & 1)
			}

			// Fast copying
			// There must be no overlap between the source and destination words

			for ok := true; ok; ok = i < match.Length {
				FastWrite(outputBuffer[outputIterator+i:], FastRead(outputBuffer[matchString+i:], WORD_SIZE), WORD_SIZE)
				i += WORD_SIZE
			}

			outputIterator += match.Length

			// Next control word bit
			controlWord >>= 1
		}
	}
}

// Retrieves information about a compressed block of data
// This operation is memory safe
// On success, returns RESULT_OK and outputs the compression information
func (d *Decompressor) GetCompressionInfo(source []byte) (Result, CompressionInfo) {
	var compressionInfo CompressionInfo

	// Decode the header
	decodeHeaderResult, header, _ := d.decodeHeader(source)

	if decodeHeaderResult != RESULT_OK {
		return decodeHeaderResult, compressionInfo
	}

	// Return the requested info
	compressionInfo.UncompressedSize = header.UncompressedSize
	compressionInfo.CompressedSize = header.CompressedSize
	compressionInfo.Version = header.Version

	return RESULT_OK, compressionInfo
}

// Decodes a match and returns its size in bytes
func (d *Decompressor) decodeMatch(source []byte) (Match, int) {
	// Read the maximum number of bytes a match is coded in (4)
	word := FastRead(source, WORD_SIZE)

	// Compute the decoding lookup table entry index: the lowest 3 bits of the encoded match
	i := word & 7

	// Compute the match offset and length using the lookup table entry
	var match Match
	match.Offset = (int)((word & d.lut[i].mask) >> d.lut[i].offsetShift)
	match.Length = (int)(((word >> uint(d.lut[i].lengthShift)) & uint(d.lut[i].lengthMask)) + MIN_MATCH_LENGTH)

	return match, int(d.lut[i].size)
}

// Decodes a header and returns its size in bytes
// If the header is not valid, the function returns 0
func (d *Decompressor) decodeHeader(source []byte) (Result, Header, int) {
	var header Header

	// Decode the attribute bytes
	if len(source) < 1 {
		return RESULT_ERROR_BUFFER_TOO_SMALL, header, 0
	}

	attributes := uint(source[0])
	source = source[1:]

	header.Version = int(attributes & 7)
	sizeCodedSize := int((attributes>>3)&7) + 1

	// Compute the size of the header
	headerSize := 1 + 2*sizeCodedSize

	if len(source) < headerSize {
		return RESULT_ERROR_BUFFER_TOO_SMALL, header, headerSize
	}

	header.IsStored = (attributes & 128) != 0

	// Decode the uncompressed and compressed sizes
	switch sizeCodedSize {
	case 1:
		header.UncompressedSize = uint64(source[0])
		header.CompressedSize = uint64(source[sizeCodedSize])

	case 2:
		header.UncompressedSize = uint64(binary.LittleEndian.Uint16(source))
		header.CompressedSize = uint64(binary.LittleEndian.Uint16(source[sizeCodedSize:]))

	case 4:
		header.UncompressedSize = uint64(binary.LittleEndian.Uint32(source))
		header.CompressedSize = uint64(binary.LittleEndian.Uint32(source[sizeCodedSize:]))

	case 8:
		header.UncompressedSize = binary.LittleEndian.Uint64(source)
		header.CompressedSize = binary.LittleEndian.Uint64(source[sizeCodedSize:])

	default:
		return RESULT_ERROR_CORRUPTED_DATA, header, headerSize
	}

	return RESULT_OK, header, headerSize
}
