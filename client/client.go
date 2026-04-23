package client

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/joho/godotenv"
)

const (
	okxBaseURL        = "https://web3.okx.com"
	oneinchBaseURL    = "https://api.1inch.dev"
	bebopBaseURL      = "https://api.bebop.xyz"
	paraswapBaseURL   = "https://api.paraswap.io"
	paraswapV5BaseURL = "https://apiv5.paraswap.io"
	openOceanBaseURL  = "https://open-api.openocean.finance"
	transitBaseURL    = "https://aggserver.transit.finance"
	kyberswapBaseURL  = "https://aggregator-api.kyberswap.com"
	lifiBaseURL       = "https://li.quest"
)

var (
	okxAPIKey         string                                                              // get from web3.okx.com/onchainos/dev-portal
	okxAPISecret      string                                                              // get from web3.okx.com/onchainos/dev-portal
	okxAPIPassphrase  string                                                              // get from web3.okx.com/onchainos/dev-portal
	okxProjectID      string                                                              // get from web3.okx.com/onchainos/dev-portal
	oneInchAPIKey     string                                                              // get from portal.1inch.dev
	taskWalletAddress = common.HexToAddress("0x000000000631cb11679942eaE370e689000494BF") // replace with your wallet address
)

func init() {
	err := godotenv.Load()
	if err != nil {
		log.Println("warn: no .env file found")
	}

	okxAPIKey = os.Getenv("OKX_API_KEY")
	okxAPISecret = os.Getenv("OKX_API_SECRET")
	okxAPIPassphrase = os.Getenv("OKX_API_PASSPHRASE")
	okxProjectID = os.Getenv("OKX_PROJECT_ID")
	oneInchAPIKey = os.Getenv("ONEINCH_API_KEY")
}

// ---------------------
// Models
// ---------------------

type QuoteParams struct {
	TokenIn    common.Address
	TokenOut   common.Address
	AmountIn   *big.Int
	Slippage   float64
	Hops       int
	Iterations int
	Protocols  []uint8
	ChainId    uint64
}

type QuoteResult struct {
	AmountOut *big.Int
	GasUsed   uint64
	Contract  common.Address
	Calldata  []byte
	Approval  common.Address
	RawResult []byte
}

type Aggregator interface {
	Name() string
	Quote(ctx context.Context, p QuoteParams) (*QuoteResult, error)   // can return Tx=nil
	BuildTx(ctx context.Context, p QuoteParams) (*QuoteResult, error) // should return Tx!=nil if supported
}

// ---------------------
// Shared HTTP helper
// ---------------------

type HTTPClient struct {
	c *http.Client
}

func NewHTTPClient(timeout time.Duration) *HTTPClient {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &HTTPClient{
		c: &http.Client{
			Timeout:   timeout,
			Transport: tr,
		},
	}
}

func (h *HTTPClient) do(ctx context.Context, req *http.Request) (*http.Response, []byte, error) {
	req = req.WithContext(ctx)
	resp, err := h.c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, body, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	return resp, body, nil
}

func mustBig(s string) (*big.Int, error) {
	if s == "" {
		return nil, errors.New("empty big int string")
	}
	x, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("invalid big int: %q", s)
	}
	return x, nil
}

// ---------------------
// Helpers
// ---------------------

func newGET(urlStr string, q url.Values) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func newPOSTJSON(urlStr string, body any) (*http.Request, []byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, urlStr, bytes.NewReader(b))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	return req, b, nil
}

func parseUint(s string) uint64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func isNative(token common.Address) bool {
	native := common.HexToAddress("0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE")
	return token == native
}

// ---------------------
// OKX Aggregator
// ---------------------

type OKX struct {
	http *HTTPClient

	apiKey        string
	apiSecret     string
	apiPassphrase string
	projectID     string
	userWallet    common.Address
}

func NewOKX(http *HTTPClient) *OKX {
	return &OKX{
		http:          http,
		apiKey:        okxAPIKey,
		apiSecret:     okxAPISecret,
		apiPassphrase: okxAPIPassphrase,
		projectID:     okxProjectID,
		userWallet:    taskWalletAddress,
	}
}

func (o *OKX) Name() string { return "OKX" }

func (o *OKX) signGET(requestPath string, params url.Values) (signature, ts string) {
	ts = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	qs := params.Encode()
	if qs != "" {
		qs = "?" + qs
	}
	preHash := ts + "GET" + requestPath + qs

	mac := hmac.New(sha256.New, []byte(o.apiSecret))
	mac.Write([]byte(preHash))
	signature = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return signature, ts
}

func (o *OKX) applyAuthHeaders(req *http.Request, signature, ts string) {
	req.Header.Set("OK-ACCESS-KEY", o.apiKey)
	req.Header.Set("OK-ACCESS-SIGN", signature)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", o.apiPassphrase)
}

func (o *OKX) Quote(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	q := url.Values{}
	q.Set("chainIndex", strconv.Itoa(int(p.ChainId)))
	q.Set("amount", p.AmountIn.String())
	q.Set("fromTokenAddress", p.TokenIn.Hex())
	q.Set("toTokenAddress", p.TokenOut.Hex())
	q.Set("swapMode", "exactIn")

	sig, ts := o.signGET("/api/v6/dex/aggregator/quote", q)

	req, err := newGET(okxBaseURL+"/api/v6/dex/aggregator/quote", q)
	if err != nil {
		return nil, fmt.Errorf("okx request: %w", err)
	}
	o.applyAuthHeaders(req, sig, ts)

	_, body, err := o.http.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("okx quote http: %w", err)
	}

	var resp struct {
		Data []struct {
			ToTokenAmount  string `json:"toTokenAmount"`
			EstimateGasFee string `json:"estimateGasFee"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("okx quote decode: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, errors.New("okx quote: empty data")
	}

	amountOut, err := mustBig(resp.Data[0].ToTokenAmount)
	if err != nil {
		return nil, fmt.Errorf("okx quote amountOut: %w", err)
	}

	return &QuoteResult{
		amountOut,
		parseUint(resp.Data[0].EstimateGasFee),
		common.Address{},
		nil,
		common.Address{},
		body,
	}, nil
}

func (o *OKX) BuildTx(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	sl := p.Slippage
	if sl >= 100 {
		sl = 99
	}

	q := url.Values{}
	q.Set("chainIndex", strconv.Itoa(int(p.ChainId)))
	q.Set("amount", p.AmountIn.String())
	q.Set("fromTokenAddress", p.TokenIn.Hex())
	q.Set("toTokenAddress", p.TokenOut.Hex())
	q.Set("swapMode", "exactIn")
	q.Set("slippagePercent", fmt.Sprintf("%.2f", sl))
	q.Set("userWalletAddress", o.userWallet.Hex())

	sig, ts := o.signGET("/api/v6/dex/aggregator/swap", q)

	req, err := newGET(okxBaseURL+"/api/v6/dex/aggregator/swap", q)
	if err != nil {
		return nil, fmt.Errorf("okx request: %w", err)
	}
	o.applyAuthHeaders(req, sig, ts)

	_, body, err := o.http.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("okx build http: %w", err)
	}

	var resp struct {
		Data []struct {
			RouterResult struct {
				ToTokenAmount string `json:"toTokenAmount"`
			} `json:"routerResult"`
			Tx struct {
				Data  string `json:"data"`
				Gas   string `json:"gas"`
				To    string `json:"to"`
				Value string `json:"value"`
			} `json:"tx"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("okx build decode: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, errors.New("okx build: empty data")
	}

	amountOut, err := mustBig(resp.Data[0].RouterResult.ToTokenAmount)
	if err != nil {
		return nil, fmt.Errorf("okx build amountOut: %w", err)
	}

	contract := common.HexToAddress(resp.Data[0].Tx.To)
	calldata := common.FromHex(resp.Data[0].Tx.Data)
	if contract == (common.Address{}) || len(calldata) == 0 {
		return nil, errors.New("okx build: missing tx.to/tx.data")
	}
	approval := common.Address{}
	if !isNative(p.TokenIn) {
		approval = contract
	}

	return &QuoteResult{
		amountOut,
		parseUint(resp.Data[0].Tx.Gas),
		contract,
		calldata,
		approval,
		body,
	}, nil
}

// ---------------------
// KyberSwap Aggregator
// ---------------------

type KyberSwap struct {
	http *HTTPClient
	user common.Address
}

func NewKyberSwap(http *HTTPClient) *KyberSwap {
	return &KyberSwap{http: http, user: taskWalletAddress}
}

func (k *KyberSwap) Name() string { return "KyberSwap" }

func (k *KyberSwap) Quote(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	q := url.Values{}
	q.Set("tokenIn", p.TokenIn.Hex())
	q.Set("tokenOut", p.TokenOut.Hex())
	q.Set("amountIn", p.AmountIn.String())
	q.Set("gasInclude", "true")

	req, err := newGET(kyberswapBaseURL+"/bsc/api/v1/routes", q)
	if err != nil {
		return nil, fmt.Errorf("kyberswap request: %w", err)
	}

	_, body, err := k.http.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("kyberswap quote http: %w", err)
	}

	var env struct {
		Data struct {
			RouteSummary  json.RawMessage `json:"routeSummary"`
			RouterAddress string          `json:"routerAddress"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("kyberswap quote decode env: %w", err)
	}
	if len(env.Data.RouteSummary) == 0 {
		return nil, fmt.Errorf("kyberswap quote: missing routeSummary, body=%s", string(body))
	}

	var rs struct {
		AmountOut string `json:"amountOut"`
		Gas       string `json:"gas"`
	}
	if err := json.Unmarshal(env.Data.RouteSummary, &rs); err != nil {
		return nil, fmt.Errorf("kyberswap quote decode routeSummary: %w", err)
	}
	if rs.AmountOut == "" {
		return nil, fmt.Errorf("kyberswap quote: missing amountOut, body=%s", string(body))
	}

	amountOut, err := mustBig(rs.AmountOut)
	if err != nil {
		return nil, fmt.Errorf("kyberswap quote amountOut: %w", err)
	}

	return &QuoteResult{
		AmountOut: amountOut,
		GasUsed:   parseUint(rs.Gas),
		Contract:  common.Address{},
		Calldata:  nil,
		Approval:  common.Address{},
		RawResult: body,
	}, nil
}

func (k *KyberSwap) BuildTx(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	quote, err := k.Quote(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(quote.RawResult) == 0 {
		return nil, errors.New("kyberswap build: missing raw quote")
	}

	var payload struct {
		Data struct {
			RouteSummary  json.RawMessage `json:"routeSummary"`
			RouterAddress string          `json:"routerAddress"`
		} `json:"data"`
	}
	if err := json.Unmarshal(quote.RawResult, &payload); err != nil {
		return nil, fmt.Errorf("kyberswap build decode quote raw: %w", err)
	}
	if len(payload.Data.RouteSummary) == 0 || payload.Data.RouterAddress == "" {
		return nil, errors.New("kyberswap build: missing routeSummary/routerAddress")
	}

	slippageBps := int(p.Slippage * 100)
	if slippageBps > 2000 {
		slippageBps = 2000
	}

	reqBody := struct {
		RouteSummary json.RawMessage `json:"routeSummary"`
		Sender       string          `json:"sender"`
		Recipient    string          `json:"recipient"`
		Slippage     int             `json:"slippageTolerance"`
		Deadline     int64           `json:"deadline"`
		Source       string          `json:"source"`
	}{
		RouteSummary: payload.Data.RouteSummary,
		Sender:       k.user.Hex(),
		Recipient:    k.user.Hex(),
		Slippage:     slippageBps,
		Deadline:     time.Now().Add(5 * time.Minute).Unix(),
	}

	req, _, err := newPOSTJSON(kyberswapBaseURL+"/bsc/api/v1/route/build", reqBody)
	if err != nil {
		return nil, fmt.Errorf("kyberswap build request: %w", err)
	}

	_, body, err := k.http.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("kyberswap build http: %w", err)
	}

	var resp struct {
		Data struct {
			AmountOut     string `json:"amountOut"`
			Gas           string `json:"gas"`
			RouterAddress string `json:"routerAddress"`
			Data          string `json:"data"`
			Value         string `json:"value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("kyberswap build decode: %w", err)
	}

	amountOut, err := mustBig(resp.Data.AmountOut)
	if err != nil {
		return nil, fmt.Errorf("kyberswap build amountOut: %w", err)
	}

	contract := common.HexToAddress(resp.Data.RouterAddress)
	calldata := common.FromHex(resp.Data.Data)
	if contract == (common.Address{}) || len(calldata) == 0 {
		return nil, errors.New("kyberswap build: missing routerAddress/data")
	}

	approval := common.Address{}
	if !isNative(p.TokenIn) {
		approval = contract
	}

	return &QuoteResult{
		AmountOut: amountOut,
		GasUsed:   parseUint(resp.Data.Gas),
		Contract:  contract,
		Calldata:  calldata,
		Approval:  approval,
		RawResult: body,
	}, nil
}

// ---------------------
// OpenOcean Aggregator
// ---------------------

type Uint64StrOrNum uint64

type OpenOcean struct {
	http     *HTTPClient
	user     common.Address
	chain    string // e.g. "bsc"
	referrer common.Address
}

func NewOpenOcean(http *HTTPClient) *OpenOcean {
	return &OpenOcean{
		http:     http,
		user:     taskWalletAddress,
		chain:    "bsc",
		referrer: common.Address{}, // set if you want attribution
	}
}

func (o *OpenOcean) Name() string { return "OpenOcean" }

func (o *OpenOcean) quoteURL() string {
	return openOceanBaseURL + "/v4/" + o.chain + "/quote"
}

func (o *OpenOcean) swapURL() string {
	return openOceanBaseURL + "/v4/" + o.chain + "/swap"
}

// Quote: /v4/{chain}/quote
func (o *OpenOcean) Quote(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	q := url.Values{}
	q.Set("inTokenAddress", p.TokenIn.Hex())
	q.Set("outTokenAddress", p.TokenOut.Hex())
	q.Set("amountDecimals", p.AmountIn.String())
	q.Set("gasPriceDecimals", "1000000000")

	req, err := newGET(o.quoteURL(), q)
	if err != nil {
		return nil, fmt.Errorf("openocean request: %w", err)
	}

	_, body, err := o.http.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("openocean quote http: %w", err)
	}

	var resp struct {
		Code int `json:"code"`
		Data struct {
			OutAmount    string         `json:"outAmount"`
			EstimatedGas Uint64StrOrNum `json:"estimatedGas"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("openocean quote decode: %w", err)
	}
	if resp.Data.OutAmount == "" {
		return nil, fmt.Errorf("openocean quote: missing outAmount, body=%s", string(body))
	}

	amountOut, err := mustBig(resp.Data.OutAmount)
	if err != nil {
		return nil, fmt.Errorf("openocean quote outAmount: %w", err)
	}

	return &QuoteResult{
		AmountOut: amountOut,
		GasUsed:   uint64(resp.Data.EstimatedGas),
		Contract:  common.Address{},
		Calldata:  nil,
		Approval:  common.Address{},
		RawResult: body,
	}, nil
}

// BuildTx: /v4/{chain}/swap (needs account=... to return tx.to + tx.data)
func (o *OpenOcean) BuildTx(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	// slippage is percent on OpenOcean
	sl := p.Slippage
	if sl <= 0 {
		sl = 0.5
	}
	if sl > 50 {
		sl = 50
	}

	q := url.Values{}
	q.Set("inTokenAddress", p.TokenIn.Hex())
	q.Set("outTokenAddress", p.TokenOut.Hex())
	q.Set("amountDecimals", p.AmountIn.String())
	q.Set("gasPriceDecimals", "1000000000")
	q.Set("slippage", fmt.Sprintf("%.4g", sl))
	q.Set("account", o.user.Hex())

	// optional attribution
	if o.referrer != (common.Address{}) {
		q.Set("referrer", o.referrer.Hex())
	}

	req, err := newGET(o.swapURL(), q)
	if err != nil {
		return nil, fmt.Errorf("openocean request: %w", err)
	}

	_, body, err := o.http.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("openocean build http: %w", err)
	}

	var resp struct {
		Code int `json:"code"`
		Data struct {
			OutAmount    string         `json:"outAmount"`
			EstimatedGas Uint64StrOrNum `json:"estimatedGas"`
			To           string         `json:"to"`
			Data         string         `json:"data"`
			Value        string         `json:"value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("openocean build decode: %w", err)
	}
	if resp.Data.OutAmount == "" {
		return nil, fmt.Errorf("openocean build: missing outAmount, body=%s", string(body))
	}

	amountOut, err := mustBig(resp.Data.OutAmount)
	if err != nil {
		return nil, fmt.Errorf("openocean build outAmount: %w", err)
	}

	contract := common.HexToAddress(resp.Data.To)
	calldata := common.FromHex(resp.Data.Data)
	if contract == (common.Address{}) || len(calldata) == 0 {
		return nil, errors.New("openocean build: missing tx.to/tx.data")
	}

	approval := common.Address{}
	if !isNative(p.TokenIn) {
		approval = contract
	}

	return &QuoteResult{
		AmountOut: amountOut,
		GasUsed:   uint64(resp.Data.EstimatedGas),
		Contract:  contract,
		Calldata:  calldata,
		Approval:  approval,
		RawResult: body,
	}, nil
}

// ---------------------
// ParaSwap (Velora) Aggregator
// ---------------------

type ParaSwap struct {
	http *HTTPClient
	user common.Address

	// cache decimals per (chainId,address)
	decCache map[string]uint8
}

func NewParaSwap(http *HTTPClient) *ParaSwap {
	return &ParaSwap{
		http:     http,
		user:     taskWalletAddress,
		decCache: make(map[string]uint8),
	}
}

func (p *ParaSwap) Name() string { return "ParaSwap" }

func (p *ParaSwap) tokenKey(chainId uint64, token common.Address) string {
	return fmt.Sprintf("%d:%s", chainId, token.Hex())
}

func (p *ParaSwap) getTokenDecimals(ctx context.Context, chainId uint64, token common.Address) (uint8, error) {
	// Paraswap uses 0xEeee... for native. Native decimals are 18 on EVM chains like BSC.
	native := common.HexToAddress("0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE")
	if token == native {
		return 18, nil
	}

	k := p.tokenKey(chainId, token)
	if d, ok := p.decCache[k]; ok {
		return d, nil
	}

	// GET https://api.paraswap.io/tokens/:network
	urlStr := fmt.Sprintf("%s/tokens/%d", paraswapBaseURL, chainId)
	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")

	_, body, err := p.http.do(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("paraswap tokens http: %w", err)
	}

	var resp struct {
		Tokens []struct {
			Address  string `json:"address"`
			Decimals uint8  `json:"decimals"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("paraswap tokens decode: %w", err)
	}

	// cache all results (avoids refetching every time)
	for _, t := range resp.Tokens {
		addr := common.HexToAddress(t.Address)
		p.decCache[p.tokenKey(chainId, addr)] = t.Decimals
	}

	if d, ok := p.decCache[k]; ok {
		return d, nil
	}
	return 0, fmt.Errorf("paraswap: token not found in tokens list: %s", token.Hex())
}

type paraswapPricesEnvelope struct {
	PriceRoute json.RawMessage `json:"priceRoute"`
}

type paraswapPriceRouteParsed struct {
	DestAmount         string `json:"destAmount"`
	GasCost            string `json:"gasCost"`
	TokenTransferProxy string `json:"tokenTransferProxy"`
}

func (p *ParaSwap) fetchPriceRoute(ctx context.Context, q QuoteParams) (raw json.RawMessage, parsed paraswapPriceRouteParsed, err error) {
	srcDec, err := p.getTokenDecimals(ctx, q.ChainId, q.TokenIn)
	if err != nil {
		return nil, parsed, err
	}
	dstDec, err := p.getTokenDecimals(ctx, q.ChainId, q.TokenOut)
	if err != nil {
		return nil, parsed, err
	}

	u, _ := url.Parse(paraswapV5BaseURL + "/prices/")
	qq := u.Query()
	qq.Set("srcToken", q.TokenIn.Hex())
	qq.Set("destToken", q.TokenOut.Hex())
	qq.Set("srcDecimals", strconv.Itoa(int(srcDec)))
	qq.Set("destDecimals", strconv.Itoa(int(dstDec)))
	qq.Set("amount", q.AmountIn.String())
	qq.Set("side", "SELL")
	qq.Set("network", strconv.Itoa(int(q.ChainId)))
	u.RawQuery = qq.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, parsed, err
	}
	req.Header.Set("Accept", "application/json")

	_, body, err := p.http.do(ctx, req)
	if err != nil {
		return nil, parsed, fmt.Errorf("paraswap prices http: %w", err)
	}

	var env paraswapPricesEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, parsed, fmt.Errorf("paraswap prices decode envelope: %w", err)
	}
	if len(env.PriceRoute) == 0 {
		return nil, parsed, fmt.Errorf("paraswap prices: missing priceRoute, body=%s", string(body))
	}

	// Parse only what we need, but KEEP env.PriceRoute untouched for /transactions.
	if err := json.Unmarshal(env.PriceRoute, &parsed); err != nil {
		return nil, parsed, fmt.Errorf("paraswap prices decode priceRoute: %w", err)
	}
	if parsed.DestAmount == "" {
		return nil, parsed, fmt.Errorf("paraswap prices: missing priceRoute.destAmount, body=%s", string(body))
	}

	return env.PriceRoute, parsed, nil
}

func (p *ParaSwap) Quote(ctx context.Context, q QuoteParams) (*QuoteResult, error) {
	priceRouteRaw, pr, err := p.fetchPriceRoute(ctx, q)
	if err != nil {
		return nil, err
	}

	amountOut, err := mustBig(pr.DestAmount)
	if err != nil {
		return nil, err
	}
	approval := common.Address{}
	if !isNative(q.TokenIn) && pr.TokenTransferProxy != "" {
		approval = common.HexToAddress(pr.TokenTransferProxy) // useful even in quote-only
	}
	return &QuoteResult{
		AmountOut: amountOut,
		GasUsed:   parseUint(pr.GasCost),
		Contract:  common.Address{},
		Calldata:  nil,
		Approval:  approval,
		RawResult: priceRouteRaw,
	}, nil
}

func (p *ParaSwap) BuildTx(ctx context.Context, q QuoteParams) (*QuoteResult, error) {
	priceRouteRaw, pr, err := p.fetchPriceRoute(ctx, q)
	if err != nil {
		return nil, err
	}

	srcDec, err := p.getTokenDecimals(ctx, q.ChainId, q.TokenIn)
	if err != nil {
		return nil, err
	}
	dstDec, err := p.getTokenDecimals(ctx, q.ChainId, q.TokenOut)
	if err != nil {
		return nil, err
	}

	// ParaSwap wants slippage in BPS in /transactions (docs)
	sl := q.Slippage
	if sl < 0 {
		sl = 0
	}
	if sl > 50 {
		sl = 50
	}
	slBps := int(sl * 100) // 1.00% => 100 bps

	txURL := fmt.Sprintf("%s/transactions/%d?ignoreChecks=true", paraswapV5BaseURL, q.ChainId)

	reqBody := map[string]any{
		"srcToken":     q.TokenIn.Hex(),
		"srcDecimals":  int(srcDec),
		"destToken":    q.TokenOut.Hex(),
		"destDecimals": int(dstDec),
		"srcAmount":    q.AmountIn.String(),
		"priceRoute":   priceRouteRaw,
		"slippage":     slBps,
		"userAddress":  p.user.Hex(),
	}

	req, _, err := newPOSTJSON(txURL, reqBody)
	if err != nil {
		return nil, fmt.Errorf("paraswap build request: %w", err)
	}

	_, body, err := p.http.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("paraswap build http: %w", err)
	}

	var txResp struct {
		To    string `json:"to"`
		Data  string `json:"data"`
		Value string `json:"value"`
		Gas   string `json:"gas"` // may be missing when ignoreChecks=true (docs)
	}
	if err := json.Unmarshal(body, &txResp); err != nil {
		return nil, fmt.Errorf("paraswap build decode: %w", err)
	}

	contract := common.HexToAddress(txResp.To)
	calldata := common.FromHex(txResp.Data)
	if contract == (common.Address{}) || len(calldata) == 0 {
		return nil, errors.New("paraswap build: missing to/data")
	}

	approval := common.Address{}
	if !isNative(q.TokenIn) {
		if pr.TokenTransferProxy != "" {
			approval = common.HexToAddress(pr.TokenTransferProxy)
		} else {
			approval = contract
		}
	}

	amountOut, _ := mustBig(pr.DestAmount)

	return &QuoteResult{
		AmountOut: amountOut,
		GasUsed:   parseUint(txResp.Gas),
		Contract:  contract,
		Calldata:  calldata,
		Approval:  approval,
		RawResult: body,
	}, nil
}

// ---------------------
// LI.FI Aggregator
// ---------------------

type LiFi struct {
	http   *HTTPClient
	user   common.Address
	apiKey string // optional; LI.FI supports x-lifi-api-key if you have one
}

func NewLiFi(http *HTTPClient) *LiFi {
	return &LiFi{
		http:   http,
		user:   taskWalletAddress,
		apiKey: "", // set if you have one
	}
}

func (l *LiFi) Name() string { return "LI.FI" }

func (l *LiFi) quoteURL() string { return lifiBaseURL + "/v1/quote" }

type liFiQuoteResp struct {
	Estimate struct {
		ToAmount        string `json:"toAmount"`
		ApprovalAddress string `json:"approvalAddress"`
		GasCosts        []struct {
			Estimate string `json:"estimate"`
			Limit    string `json:"limit"`
		} `json:"gasCosts"`
	} `json:"estimate"`
	TransactionRequest struct {
		To       string `json:"to"`
		Data     string `json:"data"`
		Value    string `json:"value"`
		GasLimit string `json:"gasLimit"`
	} `json:"transactionRequest"`
}

// Quote: GET /v1/quote
// - transactionRequest contains (to,data,value,gasLimit)
// - estimate.approvalAddress is the spender to approve for ERC20
func (l *LiFi) Quote(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	// LI.FI slippage is a DECIMAL (0.005 == 0.5%) :contentReference[oaicite:1]{index=1}
	sl := p.Slippage / 100.0
	if sl < 0 {
		sl = 0
	}
	if sl > 1 {
		sl = 1
	}

	q := url.Values{}
	q.Set("fromChain", strconv.FormatUint(p.ChainId, 10))
	q.Set("toChain", strconv.FormatUint(p.ChainId, 10)) // same-chain swap
	q.Set("fromToken", p.TokenIn.Hex())
	q.Set("toToken", p.TokenOut.Hex())
	q.Set("fromAmount", p.AmountIn.String())
	q.Set("fromAddress", l.user.Hex())
	q.Set("toAddress", l.user.Hex())
	q.Set("slippage", strconv.FormatFloat(sl, 'f', -1, 64))

	req, err := newGET(l.quoteURL(), q)
	if err != nil {
		return nil, fmt.Errorf("lifi request: %w", err)
	}
	if l.apiKey != "" {
		req.Header.Set("x-lifi-api-key", l.apiKey)
	}

	_, body, err := l.http.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("lifi quote http: %w", err)
	}

	var resp liFiQuoteResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("lifi quote decode: %w", err)
	}
	if resp.Estimate.ToAmount == "" {
		return nil, fmt.Errorf("lifi quote: missing estimate.toAmount, body=%s", string(body))
	}

	amountOut, err := mustBig(resp.Estimate.ToAmount)
	if err != nil {
		return nil, fmt.Errorf("lifi quote toAmount: %w", err)
	}

	// best-effort gas: prefer transactionRequest.gasLimit, else estimate.gasCosts[0].limit
	gas := parseUint(resp.TransactionRequest.GasLimit)
	if gas == 0 && len(resp.Estimate.GasCosts) > 0 {
		gas = parseUint(resp.Estimate.GasCosts[0].Limit)
		if gas == 0 {
			gas = parseUint(resp.Estimate.GasCosts[0].Estimate)
		}
	}
	approval := common.Address{}
	if resp.Estimate.ApprovalAddress != "" && !isNative(p.TokenIn) {
		approval = common.HexToAddress(resp.Estimate.ApprovalAddress)
	}

	return &QuoteResult{
		AmountOut: amountOut,
		GasUsed:   gas,
		Contract:  common.Address{},
		Calldata:  nil,
		Approval:  approval,
		RawResult: body,
	}, nil
}

func (l *LiFi) BuildTx(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	// get fresh quote with transactionRequest
	q := url.Values{}
	sl := p.Slippage / 100.0
	if sl < 0 {
		sl = 0
	}
	if sl > 1 {
		sl = 1
	}

	q.Set("fromChain", strconv.FormatUint(p.ChainId, 10))
	q.Set("toChain", strconv.FormatUint(p.ChainId, 10))
	q.Set("fromToken", p.TokenIn.Hex())
	q.Set("toToken", p.TokenOut.Hex())
	q.Set("fromAmount", p.AmountIn.String())
	q.Set("fromAddress", l.user.Hex())
	q.Set("toAddress", l.user.Hex())
	q.Set("slippage", strconv.FormatFloat(sl, 'f', -1, 64))

	req, err := newGET(l.quoteURL(), q)
	if err != nil {
		return nil, fmt.Errorf("lifi request: %w", err)
	}
	if l.apiKey != "" {
		req.Header.Set("x-lifi-api-key", l.apiKey)
	}

	_, body, err := l.http.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("lifi build http: %w", err)
	}

	var resp liFiQuoteResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("lifi build decode: %w", err)
	}
	if resp.Estimate.ToAmount == "" {
		return nil, fmt.Errorf("lifi build: missing estimate.toAmount, body=%s", string(body))
	}

	amountOut, err := mustBig(resp.Estimate.ToAmount)
	if err != nil {
		return nil, fmt.Errorf("lifi build toAmount: %w", err)
	}

	contract := common.HexToAddress(resp.TransactionRequest.To)
	calldata := common.FromHex(resp.TransactionRequest.Data)
	if contract == (common.Address{}) || len(calldata) == 0 {
		return nil, errors.New("lifi build: missing tx.to/tx.data")
	}

	approval := common.Address{}
	if resp.Estimate.ApprovalAddress != "" && !isNative(p.TokenIn) {
		approval = common.HexToAddress(resp.Estimate.ApprovalAddress)
	}

	gas := parseUint(resp.TransactionRequest.GasLimit)
	if gas == 0 && len(resp.Estimate.GasCosts) > 0 {
		gas = parseUint(resp.Estimate.GasCosts[0].Limit)
		if gas == 0 {
			gas = parseUint(resp.Estimate.GasCosts[0].Estimate)
		}
	}

	return &QuoteResult{
		AmountOut: amountOut,
		GasUsed:   gas,
		Contract:  contract,
		Calldata:  calldata,
		Approval:  approval,
		RawResult: body,
	}, nil
}

// ---------------------
// 1inch Classic (v6 Swap API)
// ---------------------

type OneInch struct {
	http    *HTTPClient
	user    common.Address
	chainId uint64
	apiKey  string
}

func NewOneInch(http *HTTPClient) *OneInch {
	return &OneInch{
		http:    http,
		user:    taskWalletAddress,
		chainId: 56,
		apiKey:  oneInchAPIKey,
	}
}

func (o *OneInch) Name() string { return "1inch" }

func (o *OneInch) baseURL() string {
	return fmt.Sprintf("%s/swap/v6.0/%d", oneinchBaseURL, o.chainId)
}

func (o *OneInch) setAuth(req *http.Request) {
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
}

func (o *OneInch) Quote(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	q := url.Values{}
	q.Set("src", p.TokenIn.Hex())
	q.Set("dst", p.TokenOut.Hex())
	q.Set("amount", p.AmountIn.String())

	req, err := newGET(o.baseURL()+"/quote", q)
	if err != nil {
		return nil, fmt.Errorf("1inch request: %w", err)
	}
	o.setAuth(req)

	_, body, err := o.http.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("1inch quote http: %w", err)
	}

	var resp struct {
		DstAmount string `json:"dstAmount"`
		Gas       uint64 `json:"gas"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("1inch quote decode: %w", err)
	}
	if resp.DstAmount == "" {
		return nil, fmt.Errorf("1inch quote: missing dstAmount, body=%s", string(body))
	}

	amountOut, err := mustBig(resp.DstAmount)
	if err != nil {
		return nil, fmt.Errorf("1inch quote dstAmount: %w", err)
	}

	return &QuoteResult{
		AmountOut: amountOut,
		GasUsed:   resp.Gas,
		Contract:  common.Address{},
		Calldata:  nil,
		Approval:  common.Address{},
		RawResult: body,
	}, nil
}

func (o *OneInch) BuildTx(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	sl := p.Slippage
	if sl <= 0 {
		sl = 0.5
	}
	if sl > 50 {
		sl = 50
	}

	q := url.Values{}
	q.Set("src", p.TokenIn.Hex())
	q.Set("dst", p.TokenOut.Hex())
	q.Set("amount", p.AmountIn.String())
	q.Set("from", o.user.Hex())
	q.Set("slippage", strconv.FormatFloat(sl, 'f', -1, 64))
	q.Set("disableEstimate", "true") // skip on-chain simulation; we do our own

	req, err := newGET(o.baseURL()+"/swap", q)
	if err != nil {
		return nil, fmt.Errorf("1inch request: %w", err)
	}
	o.setAuth(req)

	_, body, err := o.http.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("1inch build http: %w", err)
	}

	var resp struct {
		DstAmount string `json:"dstAmount"`
		Tx        struct {
			To   string `json:"to"`
			Data string `json:"data"`
			Gas  uint64 `json:"gas"`
		} `json:"tx"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("1inch build decode: %w", err)
	}

	contract := common.HexToAddress(resp.Tx.To)
	calldata := common.FromHex(resp.Tx.Data)
	if contract == (common.Address{}) || len(calldata) == 0 {
		return nil, fmt.Errorf("1inch build: missing tx.to/tx.data, body=%s", string(body))
	}

	amountOut, err := mustBig(resp.DstAmount)
	if err != nil {
		return nil, fmt.Errorf("1inch build dstAmount: %w", err)
	}

	// The 1inch router is both the call target and the ERC-20 spender.
	approval := common.Address{}
	if !isNative(p.TokenIn) {
		approval = contract
	}

	return &QuoteResult{
		AmountOut: amountOut,
		GasUsed:   resp.Tx.Gas,
		Contract:  contract,
		Calldata:  calldata,
		Approval:  approval,
		RawResult: body,
	}, nil
}

// ---------------------
// Bebop Aggregator (JAM API)
// ---------------------

type Bebop struct {
	http *HTTPClient
	user common.Address
}

func NewBebop(http *HTTPClient) *Bebop {
	return &Bebop{http: http, user: taskWalletAddress}
}

func (b *Bebop) Name() string { return "Bebop" }

func (b *Bebop) chainName(chainId uint64) (string, error) {
	switch chainId {
	case 1:
		return "ethereum", nil
	case 56:
		return "bsc", nil
	case 137:
		return "polygon", nil
	case 42161:
		return "arbitrum", nil
	default:
		return "", fmt.Errorf("bebop: unsupported chainId %d", chainId)
	}
}

type bebopQuoteResp struct {
	BuyTokens map[string]struct {
		Amount string `json:"amount"`
	} `json:"buyTokens"`
	ApprovalTarget string `json:"approvalTarget"`
	Tx             struct {
		To   string `json:"to"`
		Data string `json:"data"`
		Gas  uint64 `json:"gas"`
	} `json:"tx"`
}

func (b *Bebop) fetch(ctx context.Context, p QuoteParams) (*bebopQuoteResp, []byte, error) {
	chain, err := b.chainName(p.ChainId)
	if err != nil {
		return nil, nil, err
	}

	sl := p.Slippage
	if sl <= 0 {
		sl = 0.5
	}
	if sl > 50 {
		sl = 50
	}

	q := url.Values{}
	q.Set("buy_tokens", p.TokenOut.Hex())
	q.Set("sell_tokens", p.TokenIn.Hex())
	q.Set("sell_amounts", p.AmountIn.String())
	q.Set("taker_address", b.user.Hex())
	q.Set("slippage", strconv.FormatFloat(sl, 'f', -1, 64))
	q.Set("gasless", "false")

	req, err := newGET(bebopBaseURL+"/jam/"+chain+"/v2/quote", q)
	if err != nil {
		return nil, nil, fmt.Errorf("bebop request: %w", err)
	}

	_, body, err := b.http.do(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("bebop http: %w", err)
	}

	var resp bebopQuoteResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, fmt.Errorf("bebop decode: %w", err)
	}
	if len(resp.BuyTokens) == 0 {
		return nil, nil, fmt.Errorf("bebop: empty buyTokens, body=%s", string(body))
	}

	return &resp, body, nil
}

func bebopFirstAmount(tokens map[string]struct {
	Amount string `json:"amount"`
}) string {
	for _, t := range tokens {
		return t.Amount
	}
	return ""
}

func (b *Bebop) Quote(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	resp, body, err := b.fetch(ctx, p)
	if err != nil {
		return nil, err
	}

	amountOut, err := mustBig(bebopFirstAmount(resp.BuyTokens))
	if err != nil {
		return nil, fmt.Errorf("bebop quote amountOut: %w", err)
	}

	approval := common.Address{}
	if resp.ApprovalTarget != "" && !isNative(p.TokenIn) {
		approval = common.HexToAddress(resp.ApprovalTarget)
	}

	return &QuoteResult{
		AmountOut: amountOut,
		GasUsed:   resp.Tx.Gas,
		Contract:  common.Address{},
		Calldata:  nil,
		Approval:  approval,
		RawResult: body,
	}, nil
}

func (b *Bebop) BuildTx(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	resp, body, err := b.fetch(ctx, p)
	if err != nil {
		return nil, err
	}

	amountOut, err := mustBig(bebopFirstAmount(resp.BuyTokens))
	if err != nil {
		return nil, fmt.Errorf("bebop build amountOut: %w", err)
	}

	contract := common.HexToAddress(resp.Tx.To)
	calldata := common.FromHex(resp.Tx.Data)
	if contract == (common.Address{}) || len(calldata) == 0 {
		return nil, fmt.Errorf("bebop build: missing tx.to/tx.data, body=%s", string(body))
	}

	approval := common.Address{}
	if resp.ApprovalTarget != "" && !isNative(p.TokenIn) {
		approval = common.HexToAddress(resp.ApprovalTarget)
	}

	return &QuoteResult{
		AmountOut: amountOut,
		GasUsed:   resp.Tx.Gas,
		Contract:  contract,
		Calldata:  calldata,
		Approval:  approval,
		RawResult: body,
	}, nil
}

// ---------------------
// Transit Aggregator
// ---------------------

type Transit struct {
	http     *HTTPClient
	user     common.Address
	decimals map[common.Address]uint8
}

func NewTransit(http *HTTPClient) *Transit {
	return &Transit{
		http:     http,
		user:     taskWalletAddress,
		decimals: map[common.Address]uint8{},
	}
}

func (t *Transit) Name() string { return "Transit" }

func (t *Transit) getTokenDecimals(token common.Address) uint8 {
	if isNative(token) {
		return 18
	}
	if d, ok := t.decimals[token]; ok {
		return d
	}
	return 18
}

func (t *Transit) transitChainFlag(chainId uint64) (string, error) {
	switch chainId {
	case 56:
		return "BSC", nil
	default:
		return "", fmt.Errorf("unsupported chainId %d", chainId)
	}
}

func (t *Transit) Quote(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	return t.BuildTx(ctx, p) // Transit doesn't separate quote vs build; both done in BuildTx
}

func (t *Transit) BuildTx(ctx context.Context, p QuoteParams) (*QuoteResult, error) {
	// Transit slippage ("impact") is in 0..10000; docs note base 1000/‱ examples.
	// We'll do percent -> bps style: 0.50% => 50 (0..10000)
	impact := int64(p.Slippage * 100.0)
	if impact < 0 {
		impact = 0
	}
	if impact > 10000 {
		impact = 10000
	}

	dec0 := t.getTokenDecimals(p.TokenIn)
	dec1 := t.getTokenDecimals(p.TokenOut)

	chainFlag, err := t.transitChainFlag(p.ChainId)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("token0", p.TokenIn.Hex())
	q.Set("token1", p.TokenOut.Hex())
	q.Set("decimal0", strconv.Itoa(int(dec0)))
	q.Set("decimal1", strconv.Itoa(int(dec1)))
	q.Set("impact", strconv.FormatInt(impact, 10))
	q.Set("part", "10") // tune as you like (max is 10)
	q.Set("amountIn", p.AmountIn.String())
	q.Set("amountOutMin", "0")    // docs: first time set 0
	q.Set("to", t.user.Hex())     // recipient
	q.Set("issuer", t.user.Hex()) // sender
	q.Set("chain", chainFlag)
	q.Set("channel", "web") // per docs

	req, err := newGET(transitBaseURL+"/v3/transit/swap", q)
	if err != nil {
		return nil, fmt.Errorf("transit request: %w", err)
	}
	req.Header.Set("User-Agent", "curl/8.0") // Transit expects curl UA
	_, body, err := t.http.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("transit http: %w", err)
	}

	var resp struct {
		Result  int    `json:"result"`
		Message string `json:"message"`
		Data    struct {
			TotalAmountOut  string `json:"totalAmountOut"`
			Aggregator      string `json:"aggregator"`
			ApproveContract string `json:"approveContract"`
			Data            string `json:"data"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("transit decode: %w", err)
	}
	if resp.Result != 0 {
		return nil, fmt.Errorf("transit error: result=%d message=%s body=%s", resp.Result, resp.Message, string(body))
	}
	if resp.Data.TotalAmountOut == "" || resp.Data.Aggregator == "" || resp.Data.Data == "" {
		return nil, fmt.Errorf("transit missing fields: body=%s", string(body))
	}

	out, err := mustBig(resp.Data.TotalAmountOut)
	if err != nil {
		return nil, fmt.Errorf("transit amountOut: %w", err)
	}

	contract := common.HexToAddress(resp.Data.Aggregator)
	calldata := common.FromHex(resp.Data.Data)
	if contract == (common.Address{}) || len(calldata) == 0 {
		return nil, errors.New("transit: invalid aggregator/data")
	}

	approval := common.Address{}
	if !isNative(p.TokenIn) {
		if resp.Data.ApproveContract != "" {
			approval = common.HexToAddress(resp.Data.ApproveContract)
		} else {
			approval = contract // fallback
		}
	}

	return &QuoteResult{
		AmountOut: out,
		GasUsed:   0, // Transit doesn't return gas estimate
		Contract:  contract,
		Calldata:  calldata,
		Approval:  approval,
		RawResult: body,
	}, nil
}
