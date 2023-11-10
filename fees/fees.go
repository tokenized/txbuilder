package fees

import (
	"math"

	"github.com/tokenized/bitcoin_interpreter"
	"github.com/tokenized/bitcoin_interpreter/agent_bitcoin_transfer"
	"github.com/tokenized/channels"
	"github.com/tokenized/channels/unlocking_data"
	"github.com/tokenized/pkg/bitcoin"
	"github.com/tokenized/pkg/wire"

	"github.com/pkg/errors"
)

const (
	// BaseTxSize is the size of the tx not including inputs and outputs.
	//   Version = 4 bytes
	//   LockTime = 4 bytes
	BaseTxSize = 8

	// InputBaseSize is the size of a tx input not including the script.
	//   Previous Transaction ID = 32 bytes
	//   Previous Transaction Output Index = 4 bytes
	//   Sequence = 4 bytes
	InputBaseSize = bitcoin.Hash32Size + 4 + 4

	// OutputBaseSize is the size of a tx output not including the script.
	OutputBaseSize = 8

	// DustInputSize is the fixed size of an input used in the calculation of the dust limit.
	// This is actually the estimated size of a P2PKH input, but is used for dust calculation of all
	// locking scripts.
	DustInputSize = 148

	PublicKeyHashPushDataSize = 21                   // 1 byte push op code + 33 byte public key
	PublicKeyPushDataSize     = 34                   // 1 byte push op code + 33 byte public key
	MaxSignatureSize          = 73                   // 72 byte sig + 1 byte sig hash type
	MaxSignaturesPushDataSize = 1 + MaxSignatureSize // 1 byte push op code + 72 byte sig + 1 byte sig hash type
)

var (
	MissingUnlockingData = errors.New("Missing Unlocking Data")
)

func EstimateFee(tx bitcoin_interpreter.TransactionWithOutputs,
	unlocker bitcoin_interpreter.Unlocker, feeRate float64) (uint64, error) {

	size, err := EstimateSize(tx, unlocker)
	if err != nil {
		return 0, errors.Wrap(err, "estimate size")
	}

	return EstimateFeeValue(size, feeRate), nil
}

func EstimateFeeValue(size int, feeRate float64) uint64 {
	return uint64(math.Ceil(float64(size) * feeRate))
}

func EstimateSize(tx bitcoin_interpreter.TransactionWithOutputs,
	unlocker bitcoin_interpreter.Unlocker) (int, error) {
	msgTx := tx.GetMsgTx()

	result := BaseTxSize + wire.VarIntSerializeSize(uint64(len(msgTx.TxIn))) +
		wire.VarIntSerializeSize(uint64(len(msgTx.TxOut)))

	for inputIndex, txin := range msgTx.TxIn {
		inputOutput, err := tx.InputOutput(inputIndex)
		if err != nil {
			return 0, errors.Wrapf(err, "input %d output", inputIndex)
		}

		inputSize, err := EstimateInputSize(txin, inputOutput.LockingScript, unlocker)
		if err != nil {
			return 0, errors.Wrapf(err, "input %d size", inputIndex)
		}

		result += inputSize
	}

	for _, txout := range msgTx.TxOut {
		result += txout.SerializeSize()
	}

	return result, nil
}

func EstimateInputSize(txin *wire.TxIn, lockingScript bitcoin.Script,
	unlocker bitcoin_interpreter.Unlocker) (int, error) {
	if len(lockingScript) != 0 {
		// Estimate the size of the unlocking script.
		unlockingSize, err := unlocker.UnlockingSize(lockingScript)
		if err != nil {
			return 0, errors.Wrapf(err, "unlocking size")
		}

		return InputBaseSize + // outpoint + sequence
				wire.VarIntSerializeSize(uint64(unlockingSize)) + unlockingSize, // script
			nil
	}

	// The locking script is not known so the only way we can estimate the final size is if
	// unlocking data has been encoded in the unlocking script.
	if !txin.UnlockingScript.IsFalseOpReturn() {
		return 0, MissingUnlockingData
	}

	protocols := channels.NewProtocols(unlocking_data.NewProtocol())
	msg, _, err := protocols.Parse(txin.UnlockingScript)
	if err != nil {
		return 0, MissingUnlockingData
	}

	unlockingData, ok := msg.(*unlocking_data.UnlockingData)
	if !ok {
		return 0, MissingUnlockingData
	}

	return InputBaseSize + // outpoint + sequence
			wire.VarIntSerializeSize(unlockingData.Size) + int(unlockingData.Size), // script
		nil
}

func EstimateUnlockingSize(lockingScript bitcoin.Script) (int, error) {
	if lockingScript.IsP2PK() {
		// Only a signature in a P2PK unlocking script
		return MaxSignaturesPushDataSize, nil
	}

	if lockingScript.IsP2PKH() {
		// Signature and a public key in a P2PKH unlocking script
		return MaxSignaturesPushDataSize + PublicKeyPushDataSize, nil
	}

	if required, total, err := lockingScript.MultiPKHCounts(); err == nil {
		return int(total-required) + // OP_FALSE for all signatures not included
			// Signature, public key and OP_TRUE for each required signature
			int(required)*(MaxSignaturesPushDataSize+PublicKeyPushDataSize+1), nil
	}

	if info, err := agent_bitcoin_transfer.MatchScript(lockingScript); err != nil {
		agentUnlockingSize, err := EstimateUnlockingSize(info.AgentLockingScript)
		if err != nil {
			return 0, errors.Wrap(err, "agent")
		}

		return agent_bitcoin_transfer.ApproveUnlockingSize(agentUnlockingSize), nil
	}

	return 0, bitcoin.ErrWrongScriptTemplate
}

func Fee(tx bitcoin_interpreter.TransactionWithOutputs) (int64, error) {
	inputsValue, err := InputsValue(tx)
	if err != nil {
		return 0, err
	}

	outputsValue := OutputsValue(tx)

	return int64(inputsValue) - int64(outputsValue), nil
}

func InputsValue(tx bitcoin_interpreter.TransactionWithOutputs) (uint64, error) {
	msgTx := tx.GetMsgTx()
	result := uint64(0)

	for inputIndex, txin := range msgTx.TxIn {
		inputOutput, err := tx.InputOutput(inputIndex)
		if err != nil {
			return 0, errors.Wrapf(err, "input %d output", inputIndex)
		}

		inputValue, err := InputValue(txin, inputOutput)
		if err != nil {
			return 0, errors.Wrapf(err, "input %d value", inputIndex)
		}

		result += inputValue
	}

	return result, nil
}

func OutputsValue(tx bitcoin_interpreter.TransactionWithOutputs) uint64 {
	msgTx := tx.GetMsgTx()
	result := uint64(0)

	for _, txout := range msgTx.TxOut {
		result += txout.Value
	}

	return result
}

func InputValue(txin *wire.TxIn, inputOutput *wire.TxOut) (uint64, error) {
	if inputOutput.Value != 0 {
		return inputOutput.Value, nil
	}

	if !txin.UnlockingScript.IsFalseOpReturn() {
		return 0, MissingUnlockingData
	}

	protocols := channels.NewProtocols(unlocking_data.NewProtocol())
	msg, _, err := protocols.Parse(txin.UnlockingScript)
	if err != nil {
		return 0, MissingUnlockingData
	}

	unlockingData, ok := msg.(*unlocking_data.UnlockingData)
	if !ok {
		return 0, MissingUnlockingData
	}

	return unlockingData.Size, nil
}

func InputSizeForUnlockingScriptSize(unlockingScriptSize int) int {
	return InputBaseSize + // outpoint + sequence
		wire.VarIntSerializeSize(uint64(unlockingScriptSize)) + unlockingScriptSize // unlocking script
}

func OutputSize(lockingScript bitcoin.Script) int {
	scriptSize := len(lockingScript)
	return OutputBaseSize + // value
		wire.VarIntSerializeSize(uint64(scriptSize)) + scriptSize // locking script
}

func OutputSizeForLockingScriptSize(lockingScriptSize int) int {
	return OutputBaseSize + // value
		wire.VarIntSerializeSize(uint64(lockingScriptSize)) + lockingScriptSize // locking script
}

// DustLimit calculates the dust limit for an output.
func DustLimit(outputSize int, dustFeeRate float64) uint64 {
	if dustFeeRate == 0 {
		return uint64(1)
	}

	dust := EstimateFeeValue(((outputSize + DustInputSize) * 3), dustFeeRate)
	if dust < 1 {
		return uint64(1)
	}
	return dust
}

// DustLimitForOutput calculates the dust limit
func DustLimitForOutput(output *wire.TxOut, feeRate float64) uint64 {
	return DustLimit(output.SerializeSize(), feeRate)
}

// DustLimitForLockingScript calculates the dust limit
func DustLimitForLockingScript(lockingScript bitcoin.Script, feeRate float64) uint64 {
	output := &wire.TxOut{
		LockingScript: lockingScript,
	}
	return DustLimitForOutput(output, feeRate)
}

func DustLimitForLockingScriptSize(lockingScriptSize int, feeRate float64) uint64 {
	return DustLimit(OutputSizeForLockingScriptSize(lockingScriptSize), feeRate)
}

func OutputFeeForLockingScript(lockingScript bitcoin.Script, feeRate float64) uint64 {
	output := &wire.TxOut{
		LockingScript: lockingScript,
	}
	outputSize := output.SerializeSize()

	return EstimateFeeValue(outputSize, float64(feeRate))
}

// OutputFeeAndDustForLockingScript returns the tx fee required to include the locking script as an
// output in a tx and the dust limit of that output.
func OutputFeeAndDustForLockingScript(lockingScript bitcoin.Script,
	dustFeeRate, feeRate float64) (uint64, uint64) {

	output := &wire.TxOut{
		LockingScript: lockingScript,
	}
	outputSize := output.SerializeSize()

	return EstimateFeeValue(outputSize, float64(feeRate)), DustLimit(outputSize, dustFeeRate)
}

func OutputSizeAndDustForLockingScript(lockingScript bitcoin.Script,
	dustFeeRate float64) (int, uint64) {

	output := &wire.TxOut{
		LockingScript: lockingScript,
	}
	outputSize := output.SerializeSize()

	return outputSize, DustLimit(outputSize, dustFeeRate)
}
