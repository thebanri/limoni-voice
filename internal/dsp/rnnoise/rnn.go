// Package rnnoise is a pure Go port of RNNoise v0.1 (recurrent neural network noise
// suppression for 48 kHz speech, 10 ms frames), including its trained model.
//
// Copyright (c) 2017 Mozilla, 2007-2017 Jean-Marc Valin, 2005-2017 Xiph.Org Foundation,
// 2003-2004 Mark Borgerding. Redistributed under the BSD-3-Clause license of the original.
package rnnoise

import (
	_ "embed"
	"math"
)

//go:embed model.bin
var modelBlob []byte

const (
	weightsScale = 1.0 / 256
	maxNeurons   = 128

	activationTanh    = 0
	activationSigmoid = 1
	activationRelu    = 2

	inputDenseSize    = 24
	vadGRUSize        = 24
	noiseGRUSize      = 48
	denoiseGRUSize    = 96
	denoiseOutputSize = 22
	inputSize         = 42
)

type denseLayer struct {
	bias, inputWeights  []int8
	nbInputs, nbNeurons int
	activation          int
}

type gruLayer struct {
	bias, inputWeights, recurrentWeights []int8
	nbInputs, nbNeurons                  int
	activation                           int
}

var (
	inputDense    denseLayer
	vadGRU        gruLayer
	noiseGRU      gruLayer
	denoiseGRU    gruLayer
	denoiseOutput denseLayer
	vadOutput     denseLayer
)

func init() {
	off := 0
	take := func(n int) []int8 {
		out := make([]int8, n)
		for i := range out {
			out[i] = int8(modelBlob[off+i])
		}
		off += n
		return out
	}
	inputDense = denseLayer{inputWeights: take(1008), bias: take(24), nbInputs: 42, nbNeurons: 24, activation: activationTanh}
	vadGRU = gruLayer{inputWeights: take(1728), recurrentWeights: take(1728), bias: take(72), nbInputs: 24, nbNeurons: 24, activation: activationRelu}
	noiseGRU = gruLayer{inputWeights: take(12960), recurrentWeights: take(6912), bias: take(144), nbInputs: 90, nbNeurons: 48, activation: activationRelu}
	denoiseGRU = gruLayer{inputWeights: take(32832), recurrentWeights: take(27648), bias: take(288), nbInputs: 114, nbNeurons: 96, activation: activationRelu}
	denoiseOutput = denseLayer{inputWeights: take(2112), bias: take(22), nbInputs: 96, nbNeurons: 22, activation: activationSigmoid}
	vadOutput = denseLayer{inputWeights: take(24), bias: take(1), nbInputs: 24, nbNeurons: 1, activation: activationSigmoid}
	if off != len(modelBlob) {
		panic("rnnoise: model size mismatch")
	}
}

var tansigTable = [201]float32{
	0.000000, 0.039979, 0.079830, 0.119427, 0.158649,
	0.197375, 0.235496, 0.272905, 0.309507, 0.345214,
	0.379949, 0.413644, 0.446244, 0.477700, 0.507977,
	0.537050, 0.564900, 0.591519, 0.616909, 0.641077,
	0.664037, 0.685809, 0.706419, 0.725897, 0.744277,
	0.761594, 0.777888, 0.793199, 0.807569, 0.821040,
	0.833655, 0.845456, 0.856485, 0.866784, 0.876393,
	0.885352, 0.893698, 0.901468, 0.908698, 0.915420,
	0.921669, 0.927473, 0.932862, 0.937863, 0.942503,
	0.946806, 0.950795, 0.954492, 0.957917, 0.961090,
	0.964028, 0.966747, 0.969265, 0.971594, 0.973749,
	0.975743, 0.977587, 0.979293, 0.980869, 0.982327,
	0.983675, 0.984921, 0.986072, 0.987136, 0.988119,
	0.989027, 0.989867, 0.990642, 0.991359, 0.992020,
	0.992631, 0.993196, 0.993718, 0.994199, 0.994644,
	0.995055, 0.995434, 0.995784, 0.996108, 0.996407,
	0.996682, 0.996937, 0.997172, 0.997389, 0.997590,
	0.997775, 0.997946, 0.998104, 0.998249, 0.998384,
	0.998508, 0.998623, 0.998728, 0.998826, 0.998916,
	0.999000, 0.999076, 0.999147, 0.999213, 0.999273,
	0.999329, 0.999381, 0.999428, 0.999472, 0.999513,
	0.999550, 0.999585, 0.999617, 0.999646, 0.999673,
	0.999699, 0.999722, 0.999743, 0.999763, 0.999781,
	0.999798, 0.999813, 0.999828, 0.999841, 0.999853,
	0.999865, 0.999875, 0.999885, 0.999893, 0.999902,
	0.999909, 0.999916, 0.999923, 0.999929, 0.999934,
	0.999939, 0.999944, 0.999948, 0.999952, 0.999956,
	0.999959, 0.999962, 0.999965, 0.999968, 0.999970,
	0.999973, 0.999975, 0.999977, 0.999978, 0.999980,
	0.999982, 0.999983, 0.999984, 0.999986, 0.999987,
	0.999988, 0.999989, 0.999990, 0.999990, 0.999991,
	0.999992, 0.999992, 0.999993, 0.999994, 0.999994,
	0.999994, 0.999995, 0.999995, 0.999996, 0.999996,
	0.999996, 0.999997, 0.999997, 0.999997, 0.999997,
	0.999997, 0.999998, 0.999998, 0.999998, 0.999998,
	0.999998, 0.999998, 0.999999, 0.999999, 0.999999,
	0.999999, 0.999999, 0.999999, 0.999999, 0.999999,
	0.999999, 0.999999, 0.999999, 0.999999, 0.999999,
	1.000000, 1.000000, 1.000000, 1.000000, 1.000000,
	1.000000, 1.000000, 1.000000, 1.000000, 1.000000,
	1.000000,
}

func tansigApprox(x float32) float32 {
	if !(x < 8) {
		return 1
	}
	if !(x > -8) {
		return -1
	}
	if x != x {
		return 0
	}
	sign := float32(1)
	if x < 0 {
		x = -x
		sign = -1
	}
	i := int(math.Floor(float64(.5 + 25*x)))
	x -= .04 * float32(i)
	y := tansigTable[i]
	dy := 1 - y*y
	y = y + x*dy*(1-y*x)
	return sign * y
}

func sigmoidApprox(x float32) float32 {
	return .5 + .5*tansigApprox(.5*x)
}

func relu(x float32) float32 {
	if x < 0 {
		return 0
	}
	return x
}

func activate(kind int, x float32) float32 {
	switch kind {
	case activationSigmoid:
		return sigmoidApprox(x)
	case activationTanh:
		return tansigApprox(x)
	default:
		return relu(x)
	}
}

func computeDense(layer *denseLayer, output, input []float32) {
	m, n := layer.nbInputs, layer.nbNeurons
	stride := n
	for i := 0; i < n; i++ {
		sum := float32(layer.bias[i])
		for j := 0; j < m; j++ {
			sum += float32(layer.inputWeights[j*stride+i]) * input[j]
		}
		output[i] = weightsScale * sum
	}
	for i := 0; i < n; i++ {
		output[i] = activate(layer.activation, output[i])
	}
}

func computeGRU(gru *gruLayer, state, input []float32) {
	m, n := gru.nbInputs, gru.nbNeurons
	stride := 3 * n
	var z, r, h [maxNeurons]float32
	for i := 0; i < n; i++ {
		sum := float32(gru.bias[i])
		for j := 0; j < m; j++ {
			sum += float32(gru.inputWeights[j*stride+i]) * input[j]
		}
		for j := 0; j < n; j++ {
			sum += float32(gru.recurrentWeights[j*stride+i]) * state[j]
		}
		z[i] = sigmoidApprox(weightsScale * sum)
	}
	for i := 0; i < n; i++ {
		sum := float32(gru.bias[n+i])
		for j := 0; j < m; j++ {
			sum += float32(gru.inputWeights[n+j*stride+i]) * input[j]
		}
		for j := 0; j < n; j++ {
			sum += float32(gru.recurrentWeights[n+j*stride+i]) * state[j]
		}
		r[i] = sigmoidApprox(weightsScale * sum)
	}
	for i := 0; i < n; i++ {
		sum := float32(gru.bias[2*n+i])
		for j := 0; j < m; j++ {
			sum += float32(gru.inputWeights[2*n+j*stride+i]) * input[j]
		}
		for j := 0; j < n; j++ {
			sum += float32(gru.recurrentWeights[2*n+j*stride+i]) * state[j] * r[j]
		}
		sum = activate(gru.activation, weightsScale*sum)
		h[i] = z[i]*state[i] + (1-z[i])*sum
	}
	copy(state[:n], h[:n])
}

type rnnState struct {
	vadGRUState     [vadGRUSize]float32
	noiseGRUState   [noiseGRUSize]float32
	denoiseGRUState [denoiseGRUSize]float32
}

func computeRNN(rnn *rnnState, gains []float32, vad *float32, input []float32) {
	var denseOut [maxNeurons]float32
	var noiseInput, denoiseInput [maxNeurons * 3]float32
	var vadOut [1]float32
	computeDense(&inputDense, denseOut[:], input)
	computeGRU(&vadGRU, rnn.vadGRUState[:], denseOut[:])
	computeDense(&vadOutput, vadOut[:], rnn.vadGRUState[:])
	*vad = vadOut[0]
	copy(noiseInput[:], denseOut[:inputDenseSize])
	copy(noiseInput[inputDenseSize:], rnn.vadGRUState[:])
	copy(noiseInput[inputDenseSize+vadGRUSize:], input[:inputSize])
	computeGRU(&noiseGRU, rnn.noiseGRUState[:], noiseInput[:])

	copy(denoiseInput[:], rnn.vadGRUState[:])
	copy(denoiseInput[vadGRUSize:], rnn.noiseGRUState[:])
	copy(denoiseInput[vadGRUSize+noiseGRUSize:], input[:inputSize])
	computeGRU(&denoiseGRU, rnn.denoiseGRUState[:], denoiseInput[:])
	computeDense(&denoiseOutput, gains, rnn.denoiseGRUState[:])
}
