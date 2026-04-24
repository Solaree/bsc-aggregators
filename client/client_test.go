package client

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiBold   = "\033[1m"
)

// WBNB → CAKE on BSC (both 18-decimal BEP-20 wrappers), 1 WBNB.
var testParams = QuoteParams{
	TokenIn:  common.HexToAddress("0xbb4CdB9CBd36B01bD1cBaEBF2De08d9173bc095c"), // WBNB
	TokenOut: common.HexToAddress("0x0E09FaBB73Bd3Ade0a17ECC321fD13a19e81cE82"), // CAKE
	AmountIn: new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1)),                 // 1 WBNB
	Slippage: 0.50,                                                              // 0.5%
	ChainId:  56,                                                                // BSC
}

type aggRow struct {
	name string
	q    *QuoteResult
	err  error
}

// TestAggregators_BuildTx checks that every calldata-capable aggregator returns
// a non-zero contract address and calldata, then prints a colored price comparison.
func TestAggregators_BuildTx(t *testing.T) {
	httpc := NewHTTPClient(10 * time.Second)
	aggs := []Aggregator{
		NewOKX(httpc),
		NewOneInch(httpc),
		NewKyberSwap(httpc),
		NewOpenOcean(httpc),
		NewParaSwap(httpc),
		NewLiFi(httpc),
		NewTransit(httpc),
		NewBebop(httpc),
	}

	var mu sync.Mutex
	rows := make([]aggRow, 0, len(aggs))

	for _, a := range aggs {
		a := a
		t.Run(a.Name(), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			q, err := a.BuildTx(ctx, testParams)

			mu.Lock()
			rows = append(rows, aggRow{a.Name(), q, err})
			mu.Unlock()

			if err != nil {
				t.Errorf("error: %v", err)
				return
			}
			if q.Contract == (common.Address{}) || len(q.Calldata) == 0 {
				t.Fatalf("expected contract + calldata, got contract=%s calldataLen=%d",
					q.Contract.Hex(), len(q.Calldata))
			}
		})
	}

	t.Cleanup(func() { printComparison(rows) })
}

func printComparison(rows []aggRow) {
	// Sort: successful first by AmountOut desc, then errors alphabetically.
	sort.SliceStable(rows, func(i, j int) bool {
		iOK := rows[i].err == nil && rows[i].q != nil
		jOK := rows[j].err == nil && rows[j].q != nil
		switch {
		case iOK && !jOK:
			return true
		case !iOK && jOK:
			return false
		case iOK && jOK:
			return rows[i].q.AmountOut.Cmp(rows[j].q.AmountOut) > 0
		default:
			return rows[i].name < rows[j].name
		}
	})

	// Find best AmountOut.
	var best *big.Int
	for _, r := range rows {
		if r.err == nil && r.q != nil {
			if best == nil || r.q.AmountOut.Cmp(best) > 0 {
				best = r.q.AmountOut
			}
		}
	}

	const colAgg, colOut = 12, 16
	// Route column is not capped — let it expand to the longest route so nothing is cut off.
	colRoute := 8 // minimum
	for _, r := range rows {
		if r.err == nil && r.q != nil && len(r.q.Route) > colRoute {
			colRoute = len(r.q.Route)
		}
	}
	sep := strings.Repeat("─", colAgg+colRoute+colOut+14)

	fmt.Printf("\n%s══ AGGREGATOR COMPARISON ══%s\n", ansiBold, ansiReset)
	fmt.Printf("  %-*s  %-*s  %-*s  %s\n", colAgg, "AGGREGATOR", colRoute, "ROUTE", colOut, "OUT", "DIFF")
	fmt.Println("  " + sep)

	bestF := new(big.Float)
	if best != nil {
		bestF.SetInt(best)
	}
	div := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

	for _, r := range rows {
		if r.err != nil || r.q == nil {
			msg := r.err.Error()
			// trim the leading "agg build http: http NNN: " prefix for brevity
			if i := strings.Index(msg, ": {"); i != -1 {
				msg = msg[:i]
			}
			if len(msg) > 55 {
				msg = msg[:55] + "…"
			}
			fmt.Printf(ansiRed+"  %-*s  %-*s  %-*s  FAIL: %s\n"+ansiReset,
				colAgg, r.name, colRoute, "—", colOut, "—", msg)
			continue
		}

		route := r.q.Route
		if route == "" {
			route = "—"
		}

		amtF := new(big.Float).SetInt(r.q.AmountOut)
		amtF.Quo(amtF, div)
		amtS, _ := amtF.Float64()
		outStr := fmt.Sprintf("%.6f", amtS)

		var diffStr, color string
		if best != nil && r.q.AmountOut.Cmp(best) == 0 {
			diffStr = "best ✓"
			color = ansiGreen
		} else if best != nil && best.Sign() > 0 {
			diff := new(big.Float).Sub(new(big.Float).SetInt(r.q.AmountOut), bestF)
			pct, _ := new(big.Float).Quo(diff, bestF).Float64()
			pct *= 100
			diffStr = fmt.Sprintf("%+.4f%%", pct)
			if pct >= -0.01 {
				color = ansiGreen // very close to best
			} else if pct >= -0.15 {
				color = ansiYellow // decent alternative
			} else {
				color = ansiRed // significantly worse
			}
		}

		fmt.Printf(color+"  %-*s  %-*s  %-*s  %s\n"+ansiReset,
			colAgg, r.name, colRoute, route, colOut, outStr, diffStr)
	}
	fmt.Println()
}
