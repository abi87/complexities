package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"

	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/plotutil"
	"gonum.org/v1/plot/vg"

	"github.com/ava-labs/avalanchego/ids"
	commonfees "github.com/ava-labs/avalanchego/vms/components/fees"
)

const (
	recordsLen = 7

	// not exactly the height of the first banff block, but close enough
	minBanffHeight = 2_723_845
)

type BlkHeightTime struct {
	Height uint64
	Time   uint64
}

type data struct {
	ID ids.ID
	BlkHeightTime
	Complexity commonfees.Dimensions
}

// CSV structure is assumed to be the following:
// [Blk-ID, Blk-Height, Blk-Time, [Complexities]]
// Where complexities are: [Bandwitdth, UTXOsRead, UTXOsWrite, Compute]
func readCsvFile(filePath string) []data {
	f, err := os.Open(filePath)
	if err != nil {
		log.Fatal("Unable to read input file "+filePath, err)
	}
	defer f.Close()

	csvReader := csv.NewReader(f)
	records, err := csvReader.ReadAll()
	if err != nil {
		log.Fatal("Unable to parse file as CSV for "+filePath, err)
	}

	res := make([]data, 0, len(records))

	for ri, row := range records {
		if len(row) != recordsLen {
			log.Fatalf("unexpected line %d lenght: %d", ri, len(row))
		}

		var (
			entry = data{}
			err   error
		)

		entry.ID, err = ids.FromString(row[0])
		if err != nil {
			log.Fatalf("failed processing blkID, line %d: %s", ri, err)
		}

		h, err := strconv.Atoi(row[1])
		if err != nil {
			log.Fatalf("failed processing blkHeight, line %d: %s", ri, err)
		}
		entry.Height = uint64(h)

		t, err := strconv.Atoi(row[2])
		if err != nil {
			log.Fatalf("failed processing blkTime, line %d: %s", ri, err)
		}
		entry.Time = uint64(t)

		bandwidth, err := strconv.Atoi(row[3])
		if err != nil {
			log.Fatalf("failed processing bandwidth, line %d: %s", ri, err)
		}
		utxosRead, err := strconv.Atoi(row[4])
		if err != nil {
			log.Fatalf("failed processing utxosRead, line %d: %s", ri, err)
		}
		utxosWrite, err := strconv.Atoi(row[5])
		if err != nil {
			log.Fatalf("failed processing utxosWrite, line %d: %s", ri, err)
		}
		compute, err := strconv.Atoi(row[6])
		if err != nil {
			log.Fatalf("failed processing compute, line %d: %s", ri, err)
		}
		entry.Complexity = commonfees.Dimensions{
			uint64(bandwidth),
			uint64(utxosRead),
			uint64(utxosWrite),
			uint64(compute),
		}

		res = append(res, entry)
	}

	return res
}

type interval struct {
	LowTimestamp uint64 `json:"start_time"`
	UpTimestamp  uint64 `json:"endTime_time"`

	CumulatedComplexity uint64 `json:"cumulated_complexity"`
	StartHeight         uint64 `json:"start_height"`
	BlocksCount         int    `json:"peak_width"`
	ElapsedTime         uint64 `json:"peak_duration"`
}

// returns for each dimension, the start and stop indexes of each peaks
// sorted by power, i.e. \sum_peak{complexity}/peak_time_duration
func findAllDimensionPeaks(
	records []data,
	maxComplexities, medianComplexityRate commonfees.Dimensions,
	peaksCount int,
) [][]interval {
	var (
		heightsAndTimes = pullTimesHeightsFromRecords(records)
		bandwidths      = pullComplexityFromRecords(records, commonfees.Bandwidth)
		utxosReads      = pullComplexityFromRecords(records, commonfees.UTXORead)
		utxosWrites     = pullComplexityFromRecords(records, commonfees.UTXOWrite)
		computes        = pullComplexityFromRecords(records, commonfees.Compute)
	)

	bandwitdhIntervals := findPeaks(heightsAndTimes, bandwidths, maxComplexities[commonfees.Bandwidth], medianComplexityRate[commonfees.Bandwidth])
	utxosReadIntervals := findPeaks(heightsAndTimes, utxosReads, maxComplexities[commonfees.UTXORead], medianComplexityRate[commonfees.UTXORead])
	utxosWriteIntervals := findPeaks(heightsAndTimes, utxosWrites, maxComplexities[commonfees.UTXOWrite], medianComplexityRate[commonfees.UTXOWrite])
	computeIntervals := findPeaks(heightsAndTimes, computes, maxComplexities[commonfees.Compute], medianComplexityRate[commonfees.Compute])

	return [][]interval{
		bandwitdhIntervals[max(0, len(bandwitdhIntervals)-peaksCount):],
		utxosReadIntervals[max(0, len(utxosReadIntervals)-peaksCount):],
		utxosWriteIntervals[max(0, len(utxosWriteIntervals)-peaksCount):],
		computeIntervals[max(0, len(computeIntervals)-peaksCount):],
	}
}

// Peaks are defined as follows:
// - They start when trace goes above median value
// - They finish when trace goes below the median value
// Note that median value are median rate * elapsed time among blocks
// Peaks are sorted decreasingly by cumulated complexity
func findPeaks(heightsAndTimes []BlkHeightTime, trace []uint64, cap, medianRate uint64) []interval {
	if len(heightsAndTimes) != len(trace) {
		log.Fatal("time and trance have different lenght")
	}

	var (
		res         = make([]interval, 0)
		peakStarted = false
	)

	for i := 1; i < len(trace); i++ {
		v := trace[i]
		medianValue := min(cap, medianRate*max(1, heightsAndTimes[i].Time-heightsAndTimes[i-1].Time))
		switch {
		case !peakStarted && v < medianValue:
			continue // nothing to do
		case !peakStarted && v >= medianValue:
			peakStarted = true
			res = append(
				res,
				interval{
					LowTimestamp:        heightsAndTimes[i].Time,
					UpTimestamp:         heightsAndTimes[i].Time,
					CumulatedComplexity: v,
					StartHeight:         heightsAndTimes[i].Height,
					BlocksCount:         1,
					ElapsedTime:         0,
				},
			)
		case peakStarted && v > medianValue: // peak continuing
			interval := res[len(res)-1]
			interval.UpTimestamp = heightsAndTimes[i].Time
			interval.CumulatedComplexity += v
			interval.BlocksCount += 1
			interval.ElapsedTime = heightsAndTimes[i].Time - interval.LowTimestamp
			res[len(res)-1] = interval

		case peakStarted && v <= medianValue:
			interval := res[len(res)-1]
			interval.ElapsedTime = max(1, heightsAndTimes[i].Time-interval.LowTimestamp)
			res[len(res)-1] = interval
			peakStarted = false
		}
	}

	// reverse ordering of the peaks by complexity
	sort.Slice(res, func(i, j int) bool {
		switch {
		case res[i].CumulatedComplexity < res[j].CumulatedComplexity:
			return true
		case res[i].CumulatedComplexity > res[j].CumulatedComplexity:
			return false
		default:
			// if two peaks have the same cumulated complexity, pick the most concentrated one in time
			lhsPeakPower := float64(res[i].CumulatedComplexity) / float64(res[i].ElapsedTime)
			rhsPeakPower := float64(res[j].CumulatedComplexity) / float64(res[j].ElapsedTime)
			return lhsPeakPower < rhsPeakPower
		}
	})

	return res
}

func medianComplexityRate(records []data, minHeight uint64) (uint64, commonfees.Dimensions) {
	// medianComplexityRate calculates median time among blocks and median complexity rate
	// We drop empty blocks, with no complexity, since they would skew down
	// median complexity.
	// We can skip pre-Banff blocks, whose timestamp is not in the block really

	// We return a 5 components slice with:
	// - median time among blocks
	// - median complexities
	var (
		medianBlockDelay   = uint64(0)
		medianComplexities = commonfees.Empty
	)

	noEmptyRecords := skipEmptyRecords(records)
	recordsToProcess := filterRecordsByHeight(noEmptyRecords, minHeight, math.MaxUint64)

	timeSteps, bandwitdhDeriv, utxosReadDeriv, utxosWriteDeriv, computeDeriv := derivatives(recordsToProcess)

	sort.Slice(timeSteps, func(i, j int) bool { return timeSteps[i] < timeSteps[j] })
	if mid := len(timeSteps) / 2; len(timeSteps)%2 == 0 {
		medianBlockDelay = timeSteps[mid]
	} else {
		medianBlockDelay = (timeSteps[mid] + timeSteps[mid+1]) / 2
	}

	sort.Float64s(bandwitdhDeriv)
	if mid := len(bandwitdhDeriv) / 2; len(bandwitdhDeriv)%2 == 0 {
		medianComplexities[commonfees.Bandwidth] = uint64(bandwitdhDeriv[mid])
	} else {
		medianComplexities[commonfees.Bandwidth] = uint64((bandwitdhDeriv[mid] + bandwitdhDeriv[mid+1]) / 2)
	}

	sort.Float64s(utxosReadDeriv)
	if mid := len(utxosReadDeriv) / 2; len(utxosReadDeriv)%2 == 0 {
		medianComplexities[commonfees.UTXORead] = uint64(utxosReadDeriv[mid])
	} else {
		medianComplexities[commonfees.UTXORead] = uint64((utxosReadDeriv[mid] + utxosReadDeriv[mid+1]) / 2)
	}

	sort.Float64s(utxosWriteDeriv)
	if mid := len(utxosWriteDeriv) / 2; len(utxosWriteDeriv)%2 == 0 {
		medianComplexities[commonfees.UTXORead] = uint64(utxosWriteDeriv[mid])
	} else {
		medianComplexities[commonfees.UTXORead] = uint64((utxosWriteDeriv[mid] + utxosWriteDeriv[mid+1]) / 2)
	}

	sort.Float64s(computeDeriv)
	if mid := len(computeDeriv) / 2; len(computeDeriv)%2 == 0 {
		medianComplexities[commonfees.Compute] = uint64(computeDeriv[mid])
	} else {
		medianComplexities[commonfees.Compute] = uint64((computeDeriv[mid] + computeDeriv[mid+1]) / 2)
	}

	return medianBlockDelay, medianComplexities
}

func maxComplexity(records []data) commonfees.Dimensions {
	res := commonfees.Empty
	for i := 0; i < commonfees.FeeDimensions; i++ {
		max := slices.MaxFunc(records, func(lhs, rhs data) int {
			switch {
			case lhs.Complexity[i] < rhs.Complexity[i]:
				return -1
			case lhs.Complexity[i] == rhs.Complexity[i]:
				return 0
			default:
				return 1
			}
		})
		res[i] = max.Complexity[i]
	}

	// TODO: return blkIDs as well
	return res
}

func derivatives(records []data) ([]uint64, []float64, []float64, []float64, []float64) {
	timeSteps := make([]uint64, 0, len(records)-1)
	bandwitdhDeriv := make([]float64, 0, len(records)-1)
	utxosReadDeriv := make([]float64, 0, len(records)-1)
	utxosWriteDeriv := make([]float64, 0, len(records)-1)
	computeDeriv := make([]float64, 0, len(records)-1)

	for i := 1; i < len(records); i++ {
		dX := records[i].Time - records[i-1].Time
		if dX == 0 {
			dX = 1
		}
		timeSteps = append(timeSteps, dX)
		bandwitdhDeriv = append(bandwitdhDeriv, float64(records[i].Complexity[commonfees.Bandwidth])/float64(dX))
		utxosReadDeriv = append(utxosReadDeriv, float64(records[i].Complexity[commonfees.UTXORead])/float64(dX))
		utxosWriteDeriv = append(utxosWriteDeriv, float64(records[i].Complexity[commonfees.UTXOWrite])/float64(dX))
		computeDeriv = append(computeDeriv, float64(records[i].Complexity[commonfees.Compute])/float64(dX))
	}

	return timeSteps, bandwitdhDeriv, utxosReadDeriv, utxosWriteDeriv, computeDeriv
}

func main() {
	records := readCsvFile("./P-chain_complexities.csv")

	medianBlockDelay, medianComplexities := medianComplexityRate(records, minBanffHeight /*skip pre Banff blocks*/)
	fmt.Printf("median block delay: %v\n", medianBlockDelay)
	fmt.Printf("median complexities: %v\n", medianComplexities)
	fmt.Printf("\n")

	// historical max complexity. This may be way more than
	// the max complexity we would like to allow post E upgrade
	maxComplexities := maxComplexity(records)
	fmt.Printf("max complexities: %v\n", maxComplexities)
	fmt.Printf("\n")

	// find top peaks
	// maxComplexities = commonfees.Dimensions{40_000, 12_000, 16_000, 1_200_000}
	medianComplexityRate := commonfees.Dimensions{200, 60, 80, 600}
	topPeaks := findAllDimensionPeaks(records, maxComplexities, medianComplexityRate, 10)
	for d := uint64(0); d < commonfees.FeeDimensions; d++ {
		for i := len(topPeaks[d]) - 1; i >= 0; i-- {
			fmt.Printf("peak n° %d, dimension %s: %+v\n", len(topPeaks[d])-i, commonfees.DimensionStrings[d], topPeaks[d][i])
		}
		fmt.Printf("\n")
	}

	// plots ranges of complexities
	var (
		dimension      = commonfees.Bandwidth
		dimensionPeaks = topPeaks[dimension]
		targetPeak     = dimensionPeaks[len(dimensionPeaks)-1]

		minHeight = targetPeak.StartHeight + 1
		maxHeight = minHeight + uint64(targetPeak.BlocksCount)
		marginLow = 5
		low       = uint64(max(0, int(minHeight)-marginLow)) // minHeight - some margin

		marginUp = 15
		up       = maxHeight + uint64(marginUp) // maxHeight + some margin

		r      = filterRecordsByHeight(records, low, up)
		data   = pullComplexityFromRecords(r, dimension)
		x      = make([]uint64, len(r)) // block height or timestamp
		target = make([]uint64, len(r)) // target complexity
	)

	// // x is a synthetic dimension along which we plot data.
	// // BlockHeight would space our data points equally even if blocks are pretty distant in time.
	// // BlockTime may clusted some data points, since consecutive blocks may be the same timestamp
	// // It may also show a spike in target capacity if blocks are far in time.
	// // To ease up comprehension, we use a synthetic dimension that picks, at each point,
	// // we pick the timestamp but we artificially increment it if consecutive blocks have the same time
	// x[0] = r[0].Time
	// for i := 1; i < len(data); i++ {
	// 	x[i] = x[i-1] + max(r[i].Height-r[i-1].Height, r[i].Time-r[i-1].Time)
	// }

	for i := 0; i < len(data); i++ {
		x[i] = r[i].Height
	}

	for i := 1; i < len(data); i++ {
		target[i] = min(maxComplexities[dimension], medianComplexityRate[dimension]*(max(1, r[i].Time-r[i-1].Time)))
	}
	target[0] = target[1]

	// for _, d := range r {
	// 	fmt.Printf("%v\n", d)
	// }
	// fmt.Printf("\n")

	printImage(x, data, target, dimension)
}

func printImage(x, data, targetComplexity []uint64, d commonfees.Dimension) {
	p := plot.New()

	p.Title.Text = "peak complexities"
	p.X.Label.Text = "block heights"
	p.Y.Label.Text = "complexity"

	err := plotutil.AddLinePoints(p,
		commonfees.DimensionStrings[d], traceToPlotter(x, data),
		"Target", traceToPlotter(x, targetComplexity),
	)
	if err != nil {
		panic(err)
	}

	// Save the plot to a PNG file.
	if err := p.Save(4*vg.Inch, 4*vg.Inch, fmt.Sprintf("%s.png", commonfees.DimensionStrings[d])); err != nil {
		panic(err)
	}
}

func traceToPlotter(x, trace []uint64) plotter.XYs {
	if len(x) != len(trace) {
		panic("uneven x and y")
	}
	pts := make(plotter.XYs, len(trace))
	for i, v := range trace {
		pts[i].X = float64(x[i])
		pts[i].Y = float64(v)
	}
	return pts
}

func pullTimesHeightsFromRecords(records []data) []BlkHeightTime {
	res := make([]BlkHeightTime, 0, len(records))
	for _, r := range records {
		res = append(res, r.BlkHeightTime)
	}
	return res
}

func pullComplexityFromRecords(records []data, d commonfees.Dimension) []uint64 {
	res := make([]uint64, 0, len(records))
	for _, r := range records {
		res = append(res, r.Complexity[d])
	}
	return res
}

func skipEmptyRecords(records []data) []data {
	res := make([]data, 0, len(records))
	for _, r := range records {
		if r.Complexity != commonfees.Empty {
			res = append(res, r)
		}
	}

	return res
}

// assumes [records] is non-empty
func filterRecordsByHeight(records []data, minHeight, maxHeight uint64) []data {
	res := make([]data, 0)
	for _, r := range records {
		if r.Height >= minHeight && r.Height <= maxHeight {
			res = append(res, r)
		}
	}
	return res
}
