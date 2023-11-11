package main

import (
	"fmt"
	"os"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
)

const (
	framesToRead = 8192 // Define the number of frames to read each time
	baseFreq     = 2371
)

// generateSignChangeBits reads a WAV file and emits a stream of sign-change bits.
func generateSignChangeBits(decoder *wav.Decoder, offset bool) ([]int, error) {
	var bits []int

	var previous byte

	numChannels := decoder.NumChans
	bitDepth := decoder.BitDepth

	// Rewind if necessary
	if offset {
		_, _ = decoder.FullPCMBuffer() // Read and discard the offset
	}

	buf := &audio.IntBuffer{Data: make([]int, framesToRead), Format: &audio.Format{}}

	for {
		n, err := decoder.PCMBuffer(buf)
		if err != nil {
			return nil, err
		}

		if n == 0 || buf.Data == nil {
			break
		}

		for i := 0; i < len(buf.Data); i += int(numChannels) {
			var msb byte

			switch bitDepth {
			case 16:
				msb = byte(buf.Data[i] >> 8)
			case 24:
				msb = byte(buf.Data[i] >> 16)
			case 32:
				msb = byte(buf.Data[i] >> 24)
			default:
				return nil, fmt.Errorf("unsupported bit depth: %d", bitDepth)
			}

			signBit := msb & 0x80
			if signBit^previous != 0 {
				bits = append(bits, 1)
			} else {
				bits = append(bits, 0)
			}
			previous = signBit
		}
	}

	return bits, nil
}

// func generateBytes(bitstream []uint8, sampleRate int) ([]byte, error) {
// 	bitmasks := []byte{0x1, 0x2, 0x4, 0x8, 0x10, 0x20, 0x40, 0x80}
// 	samplesPerBit := int(float64(sampleRate)*4/baseFreq + 0.5)
// 	sample := make([]uint8, samplesPerBit)

// 	count := 0

// 	fmt.Println(samplesPerBit)

// 	for i := 0; i < samplesPerBit; i++ {
// 		sample[i] = bitstream[i]
// 	}
// 	signChanges := sum(sample)

// 	result := []byte{}
// 	i := 0
// 	for i < len(bitstream) {
// 		if bitstream[i] != 0 {
// 			signChanges++
// 		}
// 		if sample[0] != 0 {
// 			signChanges--
// 		}
// 		copy(sample, sample[1:])
// 		sample[len(sample)-1] = bitstream[i]

// 		// fmt.Println(signChanges)

// 		if signChanges <= 4 {
// 			var byteVal byte
// 			for _, mask := range bitmasks {
// 				sum := 0
// 				for j := 0; j < samplesPerBit && i+j < len(bitstream); j++ {
// 					sum += int(bitstream[i+j])
// 				}
// 				if sum > 7 {
// 					byteVal |= mask
// 				}
// 				i += samplesPerBit
// 			}

// 			// skip stop bits
// 			i += 2 * samplesPerBit

// 			// print byte as hex
// 			fmt.Printf("%02x\n", byteVal)
// 			count++
// 			fmt.Println(count)
// 			result = append(result, byteVal)
// 			for j := 0; j < samplesPerBit && i+j < len(bitstream); j++ {
// 				sample[j] = bitstream[i+j]
// 			}
// 			signChanges = sum(sample)
// 		} else {
// 			i++
// 		}
// 	}

//		return result, nil
//	}
const BaseFreq = 2371 // Set your BASE_FREQ
var BitMasks = []int{0x1, 0x2, 0x4, 0x8, 0x10, 0x20, 0x40, 0x80}

// generateBytes processes the sign change bits and assembles them into bytes.
func generateBytes(bitstream []int, framerate int) ([]byte, error) {
	framesPerBit := int(float64(framerate) * 4 / BaseFreq)
	sample := make([]int, framesPerBit) // Slice to use as a circular buffer
	var sampleIndex int                 // Current index in the sample buffer

	// Fill the initial buffer with data
	for i := 0; i < framesPerBit-1; i++ {
		sample[i] = bitstream[i]
	}

	var result []byte
	signChanges := sum(sample) // Calculate initial sum of sign changes
	bitstreamIndex := framesPerBit - 1

	for bitstreamIndex < len(bitstream) {
		val := bitstream[bitstreamIndex]

		if val > 0 {
			signChanges++
		}
		if sample[sampleIndex] > 0 {
			signChanges--
		}

		// Update the circular buffer
		sample[sampleIndex] = val
		sampleIndex = (sampleIndex + 1) % framesPerBit

		if signChanges <= 4 {
			byteVal := 0
			for _, mask := range BitMasks {
				if sum(bitstream[bitstreamIndex:bitstreamIndex+framesPerBit]) >= 7 {
					byteVal |= mask
				}
				bitstreamIndex += framesPerBit
			}
			result = append(result, byte(byteVal))

			// print hex of byte
			fmt.Printf("%02X\n", byteVal)

			// Skip the final two stop bits (advance the index)
			bitstreamIndex += 2 * framesPerBit

			// Refill the sample buffer
			for i := 0; i < framesPerBit-1 && bitstreamIndex+i < len(bitstream); i++ {
				sample[sampleIndex] = bitstream[bitstreamIndex+i]
				sampleIndex = (sampleIndex + 1) % framesPerBit
			}

			signChanges = sum(sample)

			bitstreamIndex += framesPerBit - 1
		} else {
			bitstreamIndex++
		}
	}

	return result, nil
}

// sum returns the sum of the elements in the slice.
func sum(slice []int) int {
	total := 0
	for _, v := range slice {
		total += v
	}
	return total
}

// func sum(slice []uint8) uint8 {
// 	var total uint8
// 	for _, v := range slice {
// 		total += v
// 	}
// 	return total
// }

func main() {
	wavFilePath := "123.wav"

	waveFile, err := os.Open(wavFilePath)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	defer waveFile.Close()

	decoder := wav.NewDecoder(waveFile)
	if !decoder.IsValidFile() {
		fmt.Println("invalid wav file")
		os.Exit(1)
	}

	sampleRate := decoder.SampleRate

	signBits, err := generateSignChangeBits(decoder, false)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}

	bytes, err := generateBytes(signBits, int(sampleRate))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	_ = bytes

	// changes := 0

	// for _, bit := range signBits {
	// 	if bit == 1 {
	// 		changes++
	// 	}

	// 	// if i < 2000 {
	// 	fmt.Print(bit) // Process or display the bit
	// 	// }
	// }

	// fmt.Println()
	// fmt.Println(len(signBits))
	// fmt.Println(changes)
}
