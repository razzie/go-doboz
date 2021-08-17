package doboz

const (
	HASH_TABLE_SIZE  = 1 << 20
	CHILD_COUNT      = DICTIONARY_SIZE * 2
	INVALID_POSITION = -1
	REBASE_THRESHOLD = (MaxInt - DICTIONARY_SIZE + 1) / DICTIONARY_SIZE * DICTIONARY_SIZE // must be a multiple of DICTIONARY_SIZE!
)

type Dictionary struct {
	// Buffer
	buffer                []byte // pointer to the beginning of the buffer inside which we look for matches
	bufferBase            int    // bufferBase > buffer, relative positions are necessary to support > 2 GB buffers
	matchableBufferLength int
	absolutePosition      int // position from the beginning of buffer

	// Cyclic dictionary
	hashTable []int // relative match positions to bufferBase
	children  []int // children of the binary tree nodes (relative match positions to bufferBase)
}

func (d *Dictionary) SetBuffer(buffer []byte) {
	// Set the buffer
	d.buffer = buffer
	d.absolutePosition = 0

	// Compute the matchable buffer length
	if len(d.buffer) > TAIL_LENGTH+MIN_MATCH_LENGTH {
		d.matchableBufferLength = len(d.buffer) - (TAIL_LENGTH + MIN_MATCH_LENGTH)
	} else {
		d.matchableBufferLength = 0
	}

	// Since we always store 32-bit positions in the dictionary, we need relative positions in order to support buffers larger then 2 GB
	// This can be possible, because the difference between any two positions stored in the dictionary never exceeds the size of the dictionary
	// We don't store larger (64-bit) positions, because that can significantly degrade performance
	// Initialize the relative position base pointer
	d.bufferBase = 0

	// Initialize if necessary
	if d.hashTable == nil {
		d.initialize()
	}

	// Clear the hash table
	for i := 0; i < HASH_TABLE_SIZE; i++ {
		d.hashTable[i] = INVALID_POSITION
	}
}

// Finds match candidates at the current buffer position and slides the matching window to the next character
// Call findMatches/update with increasing positions
// The match candidates are stored in the supplied array, ordered by their length (ascending)
// The return value is the number of match candidates in the array
func (d *Dictionary) FindMatches(matchCandidates []Match) int {
	// Check whether we can find matches at this position
	if d.absolutePosition >= d.matchableBufferLength {
		// Slide the matching window with one character
		d.absolutePosition++
		return 0
	}

	// Compute the maximum match length
	maxMatchLength := min(len(d.buffer)-TAIL_LENGTH-d.absolutePosition, MAX_MATCH_LENGTH)

	// Compute the position relative to the beginning of bufferBase_
	// All other positions (including the ones stored in the hash table and the binary trees) are relative too
	// From now on, we can safely ignore this position technique
	position := d.computeRelativePosition()

	// Compute the minimum match position
	minMatchPosition := 0
	if position >= DICTIONARY_SIZE {
		minMatchPosition = position - DICTIONARY_SIZE + 1
	}

	// Compute the hash value for the current string
	hashValue := d.hash(d.buffer, d.bufferBase+position) % HASH_TABLE_SIZE

	// Get the position of the first match from the hash table
	matchPosition := d.hashTable[hashValue]

	// Set the current string as the root of the binary tree corresponding to the hash table entry
	d.hashTable[hashValue] = position

	// Compute the current cyclic position in the dictionary
	cyclicInputPosition := position % DICTIONARY_SIZE

	// Initialize the references to the leaves of the new root's left and right subtrees
	leftSubtreeLeaf := cyclicInputPosition * 2
	rightSubtreeLeaf := cyclicInputPosition*2 + 1

	// Initialize the match lenghts of the lower and upper bounds of the current string (lowMatch < match < highMatch)
	// We use these to avoid unneccesary character comparisons at the beginnings of the strings
	lowMatchLength := 0
	highMatchLength := 0

	// Initialize the longest match length
	longestMatchLength := 0

	// Find matches
	// We look for the current string in the binary search tree and we rebuild the tree at the same time
	// The deeper a node is in the tree, the lower is its position, so the root is the string with the highest position (lowest offset)

	// We count the number of match attempts, and exit if it has reached a certain threshold
	matchCount := 0

	// Match candidates are matches which are longer than any previously encountered ones
	matchCandidateCount := 0

	for {
		// Check whether the current match position is valid
		if matchPosition < minMatchPosition || matchCount == MAX_MATCH_CANDIDATE_COUNT {
			// We have checked all valid matches, so finish the new tree and exit
			d.children[leftSubtreeLeaf] = INVALID_POSITION
			d.children[rightSubtreeLeaf] = INVALID_POSITION
			break
		}

		matchCount++

		// Compute the cyclic position of the current match in the dictionary
		cyclicMatchPosition := matchPosition % DICTIONARY_SIZE

		// Use the match lengths of the low and high bounds to determine the number of characters that surely match
		matchLength := min(lowMatchLength, highMatchLength)

		// Determine the match length
		for matchLength < maxMatchLength && d.buffer[d.bufferBase+position+matchLength] == d.buffer[d.bufferBase+matchPosition+matchLength] {
			matchLength++
		}

		// Check whether this match is the longest so far
		matchOffset := position - matchPosition

		if matchLength > longestMatchLength && matchLength >= MIN_MATCH_LENGTH {
			longestMatchLength = matchLength

			// Add the current best match to the list of good match candidates
			if matchCandidates != nil {
				matchCandidates[matchCandidateCount].Length = matchLength
				matchCandidates[matchCandidateCount].Offset = matchOffset
				matchCandidateCount++
			}

			// If the match length is the maximum allowed value, the current string is already inserted into the tree: the current node
			if matchLength == maxMatchLength {
				// Since the current string is also the root of the tree, delete the current node
				d.children[leftSubtreeLeaf] = d.children[cyclicMatchPosition*2]
				d.children[rightSubtreeLeaf] = d.children[cyclicMatchPosition*2+1]
				break
			}
		}

		// Compare the two strings
		if d.buffer[d.bufferBase+position+matchLength] < d.buffer[d.bufferBase+matchPosition+matchLength] {
			// Insert the matched string into the right subtree
			d.children[rightSubtreeLeaf] = matchPosition

			// Go left
			rightSubtreeLeaf = cyclicMatchPosition * 2
			matchPosition = d.children[rightSubtreeLeaf]

			// Update the match length of the high bound
			highMatchLength = matchLength
		} else {
			// Insert the matched string into the left subtree
			d.children[leftSubtreeLeaf] = matchPosition

			// Go right
			leftSubtreeLeaf = cyclicMatchPosition*2 + 1
			matchPosition = d.children[leftSubtreeLeaf]

			// Update the match length of the low bound
			lowMatchLength = matchLength
		}
	}

	// Slide the matching window with one character
	d.absolutePosition++

	return matchCandidateCount
}

// Slides the matching window to the next character without looking for matches, but it still has to update the dictionary
func (d *Dictionary) Skip() {
	d.FindMatches(nil)
}

func (d *Dictionary) Position() int {
	return d.absolutePosition
}

func (d *Dictionary) initialize() {
	// Create the hash table
	d.hashTable = make([]int, HASH_TABLE_SIZE)

	// Create the tree nodes
	// The number of nodes is equal to the size of the dictionary, and every node has two children
	d.children = make([]int, CHILD_COUNT)
}

// Increments the match window position with one character
func (d *Dictionary) computeRelativePosition() int {
	position := d.absolutePosition - d.bufferBase

	// Check whether the current position has reached the rebase threshold
	if position == REBASE_THRESHOLD {
		// Rebase
		rebaseDelta := REBASE_THRESHOLD - DICTIONARY_SIZE

		d.bufferBase += rebaseDelta
		position -= rebaseDelta

		// Rebase the hash entries
		for i := 0; i < HASH_TABLE_SIZE; i++ {
			if d.hashTable[i] >= rebaseDelta {
				d.hashTable[i] = d.hashTable[i] - rebaseDelta
			} else {
				d.hashTable[i] = INVALID_POSITION
			}
		}

		// Rebase the binary tree nodes
		for i := 0; i < CHILD_COUNT; i++ {
			if d.children[i] >= rebaseDelta {
				d.children[i] = d.children[i] - rebaseDelta
			} else {
				d.children[i] = INVALID_POSITION
			}
		}
	}

	return position
}

func (d *Dictionary) hash(data []byte, pos int) uint {
	// FNV-1a hash
	const prime uint = 16777619
	var result uint = 2166136261

	result = (result ^ uint(data[pos+0])) * prime
	result = (result ^ uint(data[pos+1])) * prime
	result = (result ^ uint(data[pos+2])) * prime

	return result
}
