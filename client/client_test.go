package client

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

var testParams = QuoteParams{
	TokenIn:  common.HexToAddress("0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d"), // USDC
	TokenOut: common.HexToAddress("0x55d398326f99059fF775485246999027B3197955"), // USDT
	AmountIn: new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1)),                 // 1 USDC
	Slippage: 0.50,                                                              // 0.5%
	ChainId:  56,                                                                // BSC
}

// TestAggregators_BuildTx checks that every calldata-capable aggregator returns
// a non-zero contract address and calldata.
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

	for _, a := range aggs {
		a := a
		t.Run(a.Name(), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			q, err := a.BuildTx(ctx, testParams)
			if err != nil {
				t.Errorf("error: %v", err)
				return
			}
			if q.Contract == (common.Address{}) || len(q.Calldata) == 0 {
				t.Fatalf("expected contract + calldata, got contract=%s calldataLen=%d",
					q.Contract.Hex(), len(q.Calldata))
			}
			t.Logf("contract=%s calldataLen=%d out=%s approval=%s",
				q.Contract.Hex(), len(q.Calldata), q.AmountOut.String(), q.Approval.Hex())
		})
	}
}
