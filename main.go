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
	"github.com/ava-labs/avalanchego/utils/units"

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

type rawData struct {
	ID ids.ID
	BlkHeightTime
	Complexity commonfees.Dimensions
}

type feeData struct {
	BlkHeightTime
	feeRates commonfees.Dimensions
	fee      float64 // in Avax
}

func calculateFeeData(records []rawData, feeCfg commonfees.DynamicFeesConfig) []feeData {
	res := make([]feeData, 0, len(records))

	initialFeeMan := commonfees.NewManager(feeCfg.InitialFeeRate)
	fee, err := initialFeeMan.CalculateFee(records[0].Complexity)
	if err != nil {
		panic(fmt.Sprintf("failed computing initial fee from fee rates, %s", err))
	}

	res = append(res, feeData{
		BlkHeightTime: records[0].BlkHeightTime,
		feeRates:      feeCfg.InitialFeeRate,
		fee:           float64(fee) / float64(units.Avax),
	})
	for i := 1; i < len(records); i++ {
		var (
			r                   = records[i]
			parentBlkTime       = int64(records[i-1].Time)
			parentBlkComplexity = records[i-1].Complexity
			parentFeeRates      = res[len(res)-1].feeRates

			blkTime       = int64(r.Time)
			blkComplexity = r.Complexity
		)

		feeMan := commonfees.NewManager(parentFeeRates)
		if err := feeMan.UpdateFeeRates(
			feeCfg,
			parentBlkComplexity,
			parentBlkTime,
			blkTime,
		); err != nil {
			panic(fmt.Sprintf("failed updating fee rates, %s", err))
		}

		fee, err := feeMan.CalculateFee(blkComplexity)
		if err != nil {
			panic(fmt.Sprintf("failed computing fee from fee rates, %s", err))
		}

		res = append(res, feeData{
			BlkHeightTime: r.BlkHeightTime,
			feeRates:      feeMan.GetFeeRates(),
			fee:           float64(fee) / float64(units.Avax),
		})
	}

	return res
}

// CSV structure is assumed to be the following:
// [Blk-ID, Blk-Height, Blk-Time, [Complexities]]
// Where complexities are: [Bandwitdth, UTXOsRead, UTXOsWrite, Compute]
func readCsvFile(filePath string) []rawData {
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

	res := make([]rawData, 0, len(records))

	for ri, row := range records {
		if len(row) != recordsLen {
			log.Fatalf("unexpected line %d lenght: %d", ri, len(row))
		}

		var (
			entry = rawData{}
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

type peakData struct {
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
	records []rawData,
	maxComplexities, medianComplexityRate commonfees.Dimensions,
	peaksCount int,
) [][]peakData {
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

	return [][]peakData{
		bandwitdhIntervals[max(0, len(bandwitdhIntervals)-peaksCount):],
		utxosReadIntervals[max(0, len(utxosReadIntervals)-peaksCount):],
		utxosWriteIntervals[max(0, len(utxosWriteIntervals)-peaksCount):],
		computeIntervals[max(0, len(computeIntervals)-peaksCount):],
	}
}

// Peaks are defined as follows:
// - They start when trace goes above target value
// - They finish when trace goes below the target value
// Note that target value are target rate * elapsed time among blocks
// Peaks are sorted decreasingly by cumulated complexity
func findPeaks(heightsAndTimes []BlkHeightTime, trace []uint64, cap, medianRate uint64) []peakData {
	if len(heightsAndTimes) != len(trace) {
		log.Fatal("time and trance have different lenght")
	}

	var (
		res         = make([]peakData, 0)
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
				peakData{
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

func targetComplexityRate(records []rawData, minHeight uint64, quantile float64) (uint64, commonfees.Dimensions) {
	// targetComplexityRate calculates target time among blocks and complexity rate at chosen quantile
	// We drop empty blocks, with no complexity, since they would skew down
	// target complexity.
	// We can skip pre-Banff blocks, whose timestamp is not in the block really

	// We return a 5 components slice with:
	// - median time among blocks
	// - target complexities
	var (
		medianBlockDelay   = uint64(0)
		targetComplexities = commonfees.Empty
	)

	noEmptyRecords := skipEmptyRecords(records)
	recordsToProcess := filterRecordsByHeight(noEmptyRecords, minHeight, math.MaxUint64)

	timeSteps, bandwitdhDeriv, utxosReadDeriv, utxosWriteDeriv, computeDeriv := derivatives(recordsToProcess)

	sort.Slice(timeSteps, func(i, j int) bool { return timeSteps[i] < timeSteps[j] })
	q := int(float64(len(timeSteps)) * 0.5)
	medianBlockDelay = timeSteps[q]

	sort.Float64s(bandwitdhDeriv)
	q = int(float64(len(bandwitdhDeriv)) * quantile)
	targetComplexities[commonfees.Bandwidth] = uint64(bandwitdhDeriv[q])

	sort.Float64s(utxosReadDeriv)
	q = int(float64(len(utxosReadDeriv)) * quantile)
	targetComplexities[commonfees.UTXORead] = uint64(utxosReadDeriv[q])

	sort.Float64s(utxosWriteDeriv)
	q = int(float64(len(utxosWriteDeriv)) * quantile)
	targetComplexities[commonfees.UTXOWrite] = uint64(utxosWriteDeriv[q])

	sort.Float64s(computeDeriv)
	q = int(float64(len(computeDeriv)) * quantile)
	targetComplexities[commonfees.Compute] = uint64(computeDeriv[q])

	return medianBlockDelay, targetComplexities
}

func maxComplexity(records []rawData) commonfees.Dimensions {
	res := commonfees.Empty
	for i := 0; i < commonfees.FeeDimensions; i++ {
		max := slices.MaxFunc(records, func(lhs, rhs rawData) int {
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

func derivatives(records []rawData) ([]uint64, []float64, []float64, []float64, []float64) {
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

	targetBlockDelay, targetComplexityRate := targetComplexityRate(
		records,
		minBanffHeight, /*skip pre Banff blocks*/
		0.99,           /*from 0 to 1*/
	)
	fmt.Printf("target block delay: %v\n", targetBlockDelay)
	fmt.Printf("target complexities: %v\n", targetComplexityRate)
	fmt.Printf("\n")

	// historical max complexity. This may be way more than
	// the max complexity we would like to allow post E upgrade
	maxComplexities := maxComplexity(records)
	fmt.Printf("max complexities: %v\n", maxComplexities)
	fmt.Printf("\n")

	// find top peaks
	topPeaks := findAllDimensionPeaks(records, maxComplexities, targetComplexityRate, 10)
	// for d := uint64(0); d < commonfees.FeeDimensions; d++ {
	// 	for i := len(topPeaks[d]) - 1; i >= 0; i-- {
	// 		fmt.Printf("peak n° %d, dimension %s: %+v\n", len(topPeaks[d])-i, commonfees.DimensionStrings[d], topPeaks[d][i])
	// 	}
	// 	fmt.Printf("\n")
	// }

	var (
		dimension      = commonfees.Bandwidth
		dimensionPeaks = topPeaks[dimension]
		targetPeak     = dimensionPeaks[len(dimensionPeaks)-2]

		minHeight = targetPeak.StartHeight + 1
		maxHeight = minHeight + uint64(targetPeak.BlocksCount)
		marginLow = 5
		low       = uint64(max(0, int(minHeight)-marginLow)) // minHeight - some margin

		marginUp = 3
		up       = maxHeight + uint64(marginUp) // maxHeight + some margin

		r = filterRecordsByHeight(records, low, up)
	)

	// calculate fee rates
	feeCfg := commonfees.DynamicFeesConfig{
		InitialFeeRate: commonfees.Dimensions{
			80 * units.NanoAvax,
			10 * units.NanoAvax,
			15 * units.NanoAvax,
			50 * units.NanoAvax,
		},
		MinFeeRate: commonfees.Dimensions{ // 3/4 of InitialFees
			60 * units.NanoAvax,
			8 * units.NanoAvax,
			10 * units.NanoAvax,
			35 * units.NanoAvax,
		},
		UpdateCoefficient: commonfees.Dimensions{ // over fees.CoeffDenom
			1,
			1,
			1,
			1,
		},
		BlockMaxComplexity: commonfees.Dimensions{
			100_000,
			60_000,
			60_000,
			500_000,
		},
		BlockTargetComplexityRate: commonfees.Dimensions{
			500,
			180,
			250,
			2_000,
		},
	}
	fmt.Printf("Fee config: %+v\n", feeCfg)
	allFeeRates := calculateFeeData(r, feeCfg)

	// plots ranges of complexities
	var (
		data   = pullComplexityFromRecords(r, dimension)
		x      = make([]uint64, len(r)) // block height or timestamp
		target = make([]uint64, len(r)) // target complexity
		fees   = pullFees(allFeeRates, low /*up*/, r[len(r)-1].Height)
	)

	{
		maxFee := slices.Max(fees)
		fmt.Printf("Max fee: %v Avax\n", maxFee)
		fmt.Printf("\n")
	}

	for i := 0; i < len(data); i++ {
		x[i] = r[i].Height
	}

	// // x is a synthetic dimension along which we plot data.
	// // BlockHeight would space our data points equally even if blocks are pretty distant in time.
	// // BlockTime may clusted some data points, since consecutive blocks may be the same timestamp
	// // It may also show a spike in target capacity if blocks are far in time.
	// // To ease up comprehension, we use a synthetic dimension that picks, at each point,
	// // we pick the timestamp but we artificially increment it if consecutive blocks have the same time
	// x[0] = r[0].Height
	// for i := 1; i < len(data); i++ {
	// 	x[i] = x[i-1] + max(r[i].Height-r[i-1].Height, r[i].Time-r[i-1].Time)
	// }

	for i := 1; i < len(data); i++ {
		target[i] = min(maxComplexities[dimension], targetComplexityRate[dimension]*(max(1, r[i].Time-r[i-1].Time)))
	}
	target[0] = target[1]

	printImages(x, data, target, fees, dimension)
}

func printImages(x, data, targetComplexity []uint64, fees []float64, d commonfees.Dimension) {
	p1 := plot.New()

	p1.Title.Text = "peak complexities"
	p1.X.Label.Text = "block heights"
	p1.Y.Label.Text = "complexity"

	err := plotutil.AddLinePoints(p1,
		commonfees.DimensionStrings[d], traceUint64ToPlotter(x, data),
		"target", traceUint64ToPlotter(x, targetComplexity),
	)
	if err != nil {
		panic(err)
	}

	// Save the plot to a PNG file.
	if err := p1.Save(4*vg.Inch, 4*vg.Inch, "complexities.png"); err != nil {
		panic(err)
	}

	///////////////////////////////////////////////////////////////////////////
	///////////////////////////////////////////////////////////////////////////

	p2 := plot.New()
	p2.Title.Text = "fee"
	p2.X.Label.Text = "block heights"
	p2.Y.Label.Text = "fee (Avax)"

	err = plotutil.AddLinePoints(p2,
		"fee", traceFloat64ToPlotter(x, fees),
	)
	if err != nil {
		panic(err)
	}

	// Save the plot to a PNG file.
	if err := p2.Save(4*vg.Inch, 4*vg.Inch, "fee.png"); err != nil {
		panic(err)
	}
}

func traceUint64ToPlotter(x, trace []uint64) plotter.XYs {
	if len(x) != len(trace) {
		panic("uneven x and y")
	}
	// max := slices.Max(trace)
	pts := make(plotter.XYs, len(trace))
	for i, v := range trace {
		pts[i].X = float64(x[i])
		pts[i].Y = float64(v) // / float64(max)
	}
	return pts
}

func traceFloat64ToPlotter(x []uint64, trace []float64) plotter.XYs {
	if len(x) != len(trace) {
		panic("uneven x and y")
	}
	// max := slices.Max(trace)
	pts := make(plotter.XYs, len(trace))
	for i, v := range trace {
		pts[i].X = float64(x[i])
		pts[i].Y = v // / max
	}
	return pts
}

func pullTimesHeightsFromRecords(records []rawData) []BlkHeightTime {
	res := make([]BlkHeightTime, 0, len(records))
	for _, r := range records {
		res = append(res, r.BlkHeightTime)
	}
	return res
}

func pullComplexityFromRecords(records []rawData, d commonfees.Dimension) []uint64 {
	res := make([]uint64, 0, len(records))
	for _, r := range records {
		res = append(res, r.Complexity[d])
	}
	return res
}

func pullFees(allFeeRates []feeData, low, up uint64) []float64 {
	res := make([]float64, 0, min(len(allFeeRates), int(up-low)))
	for _, data := range allFeeRates {
		if data.Height < low || data.Height > up {
			continue
		}
		res = append(res, data.fee)
	}
	return res
}

func skipEmptyRecords(records []rawData) []rawData {
	res := make([]rawData, 0, len(records))
	for _, r := range records {
		if r.Complexity != commonfees.Empty {
			res = append(res, r)
		}
	}

	return res
}

// assumes [records] is non-empty
func filterRecordsByHeight(records []rawData, minHeight, maxHeight uint64) []rawData {
	res := make([]rawData, 0)
	for _, r := range records {
		if r.Height >= minHeight && r.Height <= maxHeight {
			res = append(res, r)
		}
	}
	return res
}
