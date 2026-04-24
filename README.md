# bsc-aggregators


<img width="649" height="144" alt="Screenshot 2026-04-24 at 14 05 03" src="https://github.com/user-attachments/assets/9ef19d84-a50a-4944-80b0-d733267a14b4" />

Unified Go client for DEX aggregator APIs. Provides quote and swap building across multiple routing engines.

## Supported Aggregators

- OKX (HMAC auth, USDC→USDT quote + swap)
- 1inch (Bearer token auth, v6 swap API)
- KyberSwap (no auth, BSC routes)
- OpenOcean (no auth, slippage 0.5–50%)
- ParaSwap (token decimals fetching, slippage BPS)
- LI.FI (optional API key, decimal slippage 0–1)
- Transit (best-effort 18 decimals, 0–10000 impact)
- Bebop (JAM aggregation API, slippage 0.5–50%)

## Usage

```go
httpc := NewHTTPClient(10 * time.Second)
agg := NewBebop(httpc)

quote, err := agg.Quote(ctx, QuoteParams{
    TokenIn:  usdc,
    TokenOut: usdt,
    AmountIn: big.NewInt(1e18),
    Slippage: 0.5,
    ChainId:  56, // BSC
})
```

### Run test

```bash
go test -v -parallel=8 -run TestAggregators_BuildTx github.com/Solaree/bsc-aggregators/client
```

## API Keys

Set in `.env`:
- OKX: `OKX_API_KEY`, `OKX_API_SECRET`, `OKX_API_PASSPHRASE`, `OKX_PROJECT_ID`
- 1inch: `ONEINCH_API_KEY`
