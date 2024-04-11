package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tokenized/arc/pkg/tef"
	"github.com/tokenized/config"
	"github.com/tokenized/logger"
	"github.com/tokenized/pkg/bitcoin"
	"github.com/tokenized/pkg/wire"
	"github.com/tokenized/txbuilder"

	"github.com/pkg/errors"
)

type Config struct {
	FeeRate     float32         `default:"0.05" envconfig:"FEE_RATE" json:"fee_rate"`
	DustFeeRate float32         `default:"0.0" envconfig:"DUST_FEE_RATE" json:"dust_fee_rate"`
	Network     bitcoin.Network `default:"mainnet"`
}

func main() {
	ctx := logger.ContextWithLogger(context.Background(), true, true, "")

	cfg := &Config{}
	if err := config.LoadConfig(ctx, cfg); err != nil {
		logger.Fatal(ctx, "Failed to load config : %s", err)
	}

	maskedConfig, err := config.MarshalJSONMaskedRaw(cfg)
	if err != nil {
		logger.Fatal(ctx, "Failed to marshal config : %s", err)
	}

	logger.InfoWithFields(ctx, []logger.Field{
		logger.JSON("config", maskedConfig),
	}, "Config")

	if len(os.Args) < 2 {
		logger.Fatal(ctx, "Not enough arguments. Need command (create_send)")
	}

	switch os.Args[1] {
	case "create_send":
		CreateSend(ctx, cfg, os.Args[2:])

	case "create_sends":
		CreateSends(ctx, cfg, os.Args[2:])

	case "consolidate":
		Consolidate(ctx, cfg, os.Args[2:])
	}
}

// CreateSend creates a transaction that moves a bitcoin balance, minus the fee.
// Multiple outpoints can be specified.
// Parameters: <WIF key> <To Address> <Send Amount> <OutpointHash:Index> ...
func CreateSend(ctx context.Context, cfg *Config, args []string) {
	if len(args) < 4 {
		logger.Fatal(ctx, "Wrong argument count: create_send [Key] [Receive Address] [Amount] [Outpoints]...")
	}

	key, err := bitcoin.KeyFromStr(args[0])
	if err != nil {
		fmt.Printf("Invalid key : %s : %s\n", args[0], err)
		return
	}

	changeAddress, err := key.RawAddress()
	if err != nil {
		fmt.Printf("Failed to generate change address from key : %s\n", err)
		return
	}

	ad, err := bitcoin.DecodeAddress(args[1])
	if err != nil {
		fmt.Printf("Invalid address : %s : %s\n", args[1], err)
		return
	}
	address := bitcoin.NewRawAddressFromAddress(ad)

	amount, err := strconv.Atoi(args[2])
	if err != nil {
		fmt.Printf("Invalid amount : %s : %s\n", args[2], err)
		return
	}

	tx := txbuilder.NewTxBuilder(cfg.FeeRate, cfg.DustFeeRate)
	for i := 3; i < len(args); i++ {
		outpoint, err := wire.OutPointFromStr(args[i])
		if err != nil {
			fmt.Printf("Invalid outpoint : %s : %s\n", args[i], err)
			return
		}

		outpointTx, err := GetTx(ctx, cfg, outpoint.Hash)
		if err != nil {
			fmt.Printf("Failed to get outpoint tx : %s\n", err)
			return
		}

		if outpoint.Index >= uint32(len(outpointTx.TxOut)) {
			fmt.Printf("Invalid outpoint index : %d >= %d\n", outpoint.Index, len(outpointTx.TxOut))
			return
		}

		output := outpointTx.TxOut[outpoint.Index]
		if err := tx.AddInput(*outpoint, output.LockingScript, output.Value); err != nil {
			fmt.Printf("Failed to add spend of outpoint : %s\n", err)
			return
		}
	}

	if !changeAddress.Equal(address) {
		if err := tx.AddPaymentOutput(address, uint64(amount), false); err != nil {
			fmt.Printf("Failed to add payment output : %s\n", err)
			return
		}
	}

	if err := tx.SetChangeAddress(changeAddress, ""); err != nil {
		fmt.Printf("Failed to set change address : %s\n", err)
		return
	}

	if _, err := tx.Sign([]bitcoin.Key{key}); err != nil {
		fmt.Printf("Failed to sign transaction : %s\n", err)
		return
	}

	buf := &bytes.Buffer{}
	if err := tx.MsgTx.Serialize(buf); err != nil {
		fmt.Printf("Failed to serialize tx : %s\n", err)
		return
	}

	fmt.Printf("Tx : %s\n", tx.MsgTx.StringWithAddresses(bitcoin.MainNet))

	h := hex.EncodeToString(buf.Bytes())
	fmt.Printf("Tx Hex : %s\n", h)

	tefBuf := &bytes.Buffer{}
	if err := tef.Serialize(tefBuf, tx); err != nil {
		fmt.Printf("Failed to serialize extended tx format : %s\n", err)
		return
	}

	h = hex.EncodeToString(tefBuf.Bytes())
	fmt.Printf("Extended Tx Hex : %s\n", h)
}

// CreateSend creates a chain of transactions that moves a bitcoin balance, minus the fee.
// Multiple outpoints can be specified.
// Parameters: <WIF key> <To Address> <Send Amount> <OutpointHash:Index> ...
func CreateSends(ctx context.Context, cfg *Config, args []string) {
	if len(args) < 3 {
		logger.Fatal(ctx, "Wrong argument count: create_send [Count] [Key] [Outpoints]...")
	}

	count, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Printf("Invalid amount : %s : %s\n", args[0], err)
		return
	}

	key, err := bitcoin.KeyFromStr(args[1])
	if err != nil {
		fmt.Printf("Invalid key : %s : %s\n", args[1], err)
		return
	}

	changeAddress, err := key.RawAddress()
	if err != nil {
		fmt.Printf("Failed to generate change address from key : %s\n", err)
		return
	}

	tx := txbuilder.NewTxBuilder(cfg.FeeRate, cfg.DustFeeRate)
	for i := 2; i < len(args); i++ {
		outpoint, err := wire.OutPointFromStr(args[i])
		if err != nil {
			fmt.Printf("Invalid outpoint : %s : %s\n", args[i], err)
			return
		}

		outpointTx, err := GetTx(ctx, cfg, outpoint.Hash)
		if err != nil {
			fmt.Printf("Failed to get outpoint tx : %s\n", err)
			return
		}

		if outpoint.Index >= uint32(len(outpointTx.TxOut)) {
			fmt.Printf("Invalid outpoint index : %d >= %d\n", outpoint.Index, len(outpointTx.TxOut))
			return
		}

		output := outpointTx.TxOut[outpoint.Index]
		if err := tx.AddInput(*outpoint, output.LockingScript, output.Value); err != nil {
			fmt.Printf("Failed to add spend of outpoint : %s\n", err)
			return
		}
	}

	if err := tx.SetChangeAddress(changeAddress, ""); err != nil {
		fmt.Printf("Failed to set change address : %s\n", err)
		return
	}

	if _, err := tx.Sign([]bitcoin.Key{key}); err != nil {
		fmt.Printf("Failed to sign transaction : %s\n", err)
		return
	}

	buf := &bytes.Buffer{}
	if err := tx.MsgTx.Serialize(buf); err != nil {
		fmt.Printf("Failed to serialize tx : %s\n", err)
		return
	}

	fmt.Printf("Tx : %s\n", tx.MsgTx.StringWithAddresses(bitcoin.MainNet))

	h := hex.EncodeToString(buf.Bytes())
	fmt.Printf("Tx Hex : %s\n", h)

	tefBuf := &bytes.Buffer{}
	if err := tef.Serialize(tefBuf, tx); err != nil {
		fmt.Printf("Failed to serialize extended tx format : %s\n", err)
		return
	}

	h = hex.EncodeToString(tefBuf.Bytes())
	fmt.Printf("Extended Tx Hex : %s\n", h)

	cumulativeTefBuf := &bytes.Buffer{}
	if err := tef.Serialize(cumulativeTefBuf, tx); err != nil {
		fmt.Printf("Failed to serialize extended tx format : %s\n", err)
		return
	}

	for i := 1; i < count; i++ {
		previousTxID := *tx.MsgTx.TxHash()
		outpointTx := tx.MsgTx

		tx = txbuilder.NewTxBuilder(cfg.FeeRate, cfg.DustFeeRate)

		output := outpointTx.TxOut[0]
		if err := tx.AddInput(wire.OutPoint{previousTxID, 0}, output.LockingScript,
			output.Value); err != nil {
			fmt.Printf("Failed to add spend of outpoint : %s\n", err)
			return
		}

		if err := tx.SetChangeAddress(changeAddress, ""); err != nil {
			fmt.Printf("Failed to set change address : %s\n", err)
			return
		}

		if _, err := tx.Sign([]bitcoin.Key{key}); err != nil {
			fmt.Printf("Failed to sign transaction : %s\n", err)
			return
		}

		buf := &bytes.Buffer{}
		if err := tx.MsgTx.Serialize(buf); err != nil {
			fmt.Printf("Failed to serialize tx : %s\n", err)
			return
		}

		fmt.Printf("Tx : %s\n", tx.MsgTx.StringWithAddresses(bitcoin.MainNet))

		h := hex.EncodeToString(buf.Bytes())
		fmt.Printf("Tx Hex : %s\n", h)

		tefBuf := &bytes.Buffer{}
		if err := tef.Serialize(tefBuf, tx); err != nil {
			fmt.Printf("Failed to serialize extended tx format : %s\n", err)
			return
		}

		h = hex.EncodeToString(tefBuf.Bytes())
		fmt.Printf("Extended Tx Hex : %s\n", h)

		if err := tef.Serialize(cumulativeTefBuf, tx); err != nil {
			fmt.Printf("Failed to serialize extended tx format : %s\n", err)
			return
		}
	}

	h = hex.EncodeToString(cumulativeTefBuf.Bytes())
	fmt.Printf("Cumulative Extended Txs Hex : %s\n", h)
}

func Consolidate(ctx context.Context, cfg *Config, args []string) {
	if len(args) < 2 {
		logger.Fatal(ctx, "Wrong argument count: consolidate [Key] [Recipient Script/Address/BIP0276]")
	}

	key, err := bitcoin.KeyFromStr(args[0])
	if err != nil {
		fmt.Printf("Invalid key : %s : %s\n", args[0], err)
		return
	}

	keyScript, err := key.LockingScript()
	if err != nil {
		fmt.Printf("Failed to create key locking script : %s\n", err)
		return
	}

	scriptText := strings.Join(args[1:], " ")
	recipientScript, err := bitcoin.DecodeToLockingScript(scriptText, cfg.Network)
	if err != nil {
		fmt.Printf("Invalid recipient script : \"%s\" : %s\n", scriptText, err)
		return
	}

	utxos, err := GetUTXOs(ctx, cfg, keyScript)
	if err != nil {
		fmt.Printf("Failed to get key UTXOs : %s\n", err)
		return
	}

	tx := txbuilder.NewTxBuilder(cfg.FeeRate, cfg.DustFeeRate)

	if err := tx.AddOutput(recipientScript, 0, true, true); err != nil {
		fmt.Printf("Failed to add recipient output : %s\n", err)
		return
	}

	for _, utxo := range utxos {
		if err := tx.AddInputUTXO(utxo); err != nil {
			fmt.Printf("Failed to add input : %s\n", err)
			return
		}
	}

	if _, err := tx.Sign([]bitcoin.Key{key}); err != nil {
		fmt.Printf("Failed to sign transaction : %s\n", err)
		return
	}

	buf := &bytes.Buffer{}
	if err := tx.MsgTx.Serialize(buf); err != nil {
		fmt.Printf("Failed to serialize tx : %s\n", err)
		return
	}

	fmt.Printf("Tx : %s\n", tx.MsgTx.StringWithAddresses(bitcoin.MainNet))

	h := hex.EncodeToString(buf.Bytes())
	fmt.Printf("Tx Hex : %s\n", h)
}

type UnspentTx struct {
	BlockHeight uint           `json:"height"`  // block height
	Index       uint32         `json:"tx_pos"`  // index of output in transaction
	Hash        bitcoin.Hash32 `json:"tx_hash"` // hash of transaction
	Value       uint64         `json:"value"`   // value of output
}

func GetUTXOs(ctx context.Context, cfg *Config,
	lockingScript bitcoin.Script) ([]bitcoin.UTXO, error) {

	const urlTemplate = "https://api.whatsonchain.com/v1/bsv/%s/script/%s/unspent"

	scriptHash := bitcoin.Hash32(sha256.Sum256([]byte(lockingScript)))
	fmt.Printf("Script Hash : %s\n", scriptHash)
	url := fmt.Sprintf(urlTemplate, networkString(cfg.Network), scriptHash)

	b, err := httpGet(ctx, url)
	if err != nil {
		return nil, errors.Wrap(err, "get unspent txs")
	}

	var unspentTxs []UnspentTx
	if err := json.Unmarshal(b, &unspentTxs); err != nil {
		return nil, errors.Wrap(err, "unmarshal")
	}

	js, _ := json.MarshalIndent(unspentTxs, "", "  ")
	fmt.Printf("Unspent Txs : %s\n", js)

	utxos := make([]bitcoin.UTXO, len(unspentTxs))
	for i, unspentTx := range unspentTxs {
		utxos[i] = bitcoin.UTXO{
			Hash:          unspentTx.Hash,
			Index:         unspentTx.Index,
			Value:         unspentTx.Value,
			LockingScript: lockingScript,
		}
	}

	return utxos, nil
}

func GetTx(ctx context.Context, cfg *Config, hash bitcoin.Hash32) (*wire.MsgTx, error) {
	h, err := httpGet(ctx, fmt.Sprintf("https://api.whatsonchain.com/v1/bsv/%s/tx/%s/hex",
		networkString(cfg.Network), hash))
	if err != nil {
		return nil, errors.Wrap(err, "http get")
	}

	b, err := hex.DecodeString(string(h))
	if err != nil {
		return nil, errors.Wrap(err, "decode hex")
	}

	tx := &wire.MsgTx{}
	if err := tx.Deserialize(bytes.NewReader(b)); err != nil {
		return nil, errors.Wrap(err, "decode tx")
	}

	return tx, nil
}

func networkString(net bitcoin.Network) string {
	switch net {
	case bitcoin.MainNet:
		return "main"
	default:
		return "test"
	}
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	var transport = &http.Transport{
		Dial: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).Dial,
		TLSHandshakeTimeout: 5 * time.Second,
	}

	var client = &http.Client{
		Timeout:   time.Second * 10,
		Transport: transport,
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.Wrap(err, "create request")
	}

	httpResponse, err := client.Do(httpRequest)
	if err != nil {
		return nil, errors.Wrap(err, "http post")
	}

	if httpResponse.StatusCode < 200 || httpResponse.StatusCode > 299 {
		if httpResponse.Body != nil {
			b, rerr := ioutil.ReadAll(httpResponse.Body)
			if rerr == nil {
				return nil, fmt.Errorf("HTTP Error : %d - %s", httpResponse.StatusCode, string(b))
			}
		}

		return nil, fmt.Errorf("HTTP Error : %d", httpResponse.StatusCode)
	}

	defer httpResponse.Body.Close()

	b, err := ioutil.ReadAll(httpResponse.Body)
	if err != nil {
		return nil, errors.Wrap(err, "read response body")
	}

	return b, nil
}
