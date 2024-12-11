package txbuilder

import (
	"fmt"
	"math"

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

	PublicKeyHashPushDataSize = 21                   // 1 byte push op code + 33 byte public key
	PublicKeyPushDataSize     = 34                   // 1 byte push op code + 33 byte public key
	MaxSignatureSize          = 73                   // 72 byte sig + 1 byte sig hash type
	MaxSignaturesPushDataSize = 1 + MaxSignatureSize // 1 byte push op code + 72 byte sig + 1 byte sig hash type

	// InputBaseSize is the size of a tx input not including script
	//   Previous Transaction ID = 32 bytes
	//   Previous Transaction Output Index = 4 bytes
	//   Sequence = 4 bytes
	InputBaseSize = bitcoin.Hash32Size + 4 + 4

	// MaximumP2PKHInputSize is the maximum serialized size of a P2PKH tx input based on all of the
	// variable sized data.
	// P2PKH/P2SH input size 149
	//   Previous Transaction ID = 32 bytes
	//   Previous Transaction Output Index = 4 bytes
	//   script size = 1 byte
	//   Signature push to stack = 74
	//       push size = 1 byte
	//       signature up to = 72 bytes
	//       signature hash type = 1 byte
	//   Public key push to stack = 34
	//       push size = 1 byte
	//       public key size = 33 bytes
	//   Sequence number = 4
	MaximumP2PKHSigScriptSize = MaxSignaturesPushDataSize + PublicKeyPushDataSize
	MaximumP2PKHInputSize     = InputBaseSize + 1 + MaximumP2PKHSigScriptSize

	// MaximumP2RPHInputSize is the maximum serialized size of a P2RPH tx input based on all of the
	// variable sized data.
	// P2PKH/P2SH/P2RPH input size 149
	//   Previous Transaction ID = 32 bytes
	//   Previous Transaction Output Index = 4 bytes
	//   script size = 1 byte
	//   Public key push to stack = 34
	//       push size = 1 byte
	//       public key size = 33 bytes
	//   Signature push to stack = 74
	//       push size = 1 byte
	//       signature up to = 72 bytes
	//       signature hash type = 1 byte
	//   Sequence number = 4
	MaximumP2RPHSigScriptSize = PublicKeyPushDataSize + MaxSignaturesPushDataSize
	MaximumP2RPHInputSize     = InputBaseSize + 1 + MaximumP2RPHSigScriptSize

	// MaximumP2PKInputSize is the maximium serialized size of a P2PK tx input based on all of the
	// variable sized data.
	// P2PK input size 115
	//   Previous Transaction ID = 32 bytes
	//   Previous Transaction Output Index = 4 bytes
	//   script size = 1 byte
	//   Signature push to stack = 74
	//       push size = 1 byte
	//       signature up to = 72 bytes
	//       signature hash type = 1 byte
	//   Sequence number = 4
	MaximumP2PKSigScriptSize = MaxSignaturesPushDataSize
	MaximumP2PKInputSize     = InputBaseSize + 1 + MaximumP2PKSigScriptSize

	// OutputBaseSize is the size of a tx output not including script
	OutputBaseSize = 8

	// P2PKHOutputSize is the serialized size of a P2PKH tx output.
	// P2PKH/P2SH output size 34
	//   amount = 8 bytes
	//   script size = 1 byte
	//   Script (25 bytes) OP_DUP OP_HASH160 <Push Data byte, PUB KEY/SCRIPT HASH (20 bytes)> OP_EQUALVERIFY
	//     OP_CHECKSIG
	P2PKHOutputScriptSize = PublicKeyHashPushDataSize + 4
	P2PKHOutputSize       = OutputBaseSize + 1 + P2PKHOutputScriptSize

	// P2PKOutputSize is the serialized size of a P2PK tx output.
	// P2PK output size 44
	//   amount = 8 bytes
	//   script = 36
	//     script size = 1 byte ()
	//       Public key push to stack = 34
	//         push size = 1 byte
	//         public key size = 33 bytes
	//       OP_CHECKSIG = 1 byte
	P2PKOutputScriptSize = PublicKeyPushDataSize + 1
	P2PKOutputSize       = OutputBaseSize + 1 + P2PKOutputScriptSize

	// DustInputSize is the fixed size of an input used in the calculation of the dust limit.
	// This is actually the estimated size of a P2PKH input, but is used for dust calculation of all
	//   locking scripts.
	DustInputSize = 148
)

// UnlockingScriptSize calculates the length of the unlocking script needed to unlock the specified
// locking script.
func UnlockingScriptSize(lockingScript bitcoin.Script) (int, error) {
	if lockingScript.IsP2PK() {
		// Only a signature in a P2PK unlocking script
		return MaxSignaturesPushDataSize, nil
	}

	if lockingScript.IsP2PKH() {
		// Signature and a public key in a P2PKH unlocking script
		return MaxSignaturesPushDataSize + PublicKeyPushDataSize, nil
	}

	if required, total, err := lockingScript.MultiPKHCounts(); err == nil {
		scriptSize := int(total - required) // OP_FALSE for all signatures not included

		// Signature, public key and OP_TRUE for each required signature
		scriptSize += int(required) * (MaxSignaturesPushDataSize + PublicKeyPushDataSize + 1)

		return scriptSize, nil
	}

	if info, err := agent_bitcoin_transfer.MatchScript(lockingScript); err == nil && info != nil {
		agentUnlockingScript := info.AgentLockingScript.Copy()
		agentUnlockingScript.RemoveHardVerify()
		agentUnlockingSize, err := UnlockingScriptSize(agentUnlockingScript)
		if err != nil {
			return 0, errors.Wrap(err, "agent unlocking size")
		}

		return agent_bitcoin_transfer.ApproveUnlockingSize(agentUnlockingSize), nil
	}

	return 0, bitcoin.ErrUnknownScriptTemplate
}

// InputSize returns the serialize size in bytes of an input spending the specified locking script.
// Note: The script is not the script that would be contained in the input, but the script that
// is contained in the output being spent by this input.
func InputSize(lockingScript bitcoin.Script) (int, error) {
	scriptSize, err := UnlockingScriptSize(lockingScript)
	if err != nil {
		return 0, err
	}

	return InputBaseSize + // outpoint + sequence
		VarIntSerializeSize(uint64(scriptSize)) + scriptSize, nil // unlocking script
}

// OutputSize returns the serialize size in bytes of an output containing the specified locking
// script.
func OutputSize(lockingScript bitcoin.Script) int {
	scriptSize := len(lockingScript)
	return OutputBaseSize + // value
		VarIntSerializeSize(uint64(scriptSize)) + scriptSize // locking script
}

// The fee should be estimated before signing, then after signing the fee should be checked.
// If the fee is too low after signing, then the fee should be adjusted and the tx re-signed.

func (tx *TxBuilder) Fee() uint64 {
	o := tx.OutputValue(true)
	i := tx.InputValue()
	if o > i {
		return 0
	}
	return i - o
}

func (tx *TxBuilder) ActualFee() int64 {
	o := int64(tx.OutputValue(true))
	i := int64(tx.InputValue())
	return i - o
}

// EstimatedSize returns the estimated size in bytes of the tx after signatures are added.
// It assumes all inputs are P2PKH, P2PK, or P2RPH.
func (tx *TxBuilder) EstimatedSize() int {
	result := BaseTxSize + wire.VarIntSerializeSize(uint64(len(tx.MsgTx.TxIn))) +
		wire.VarIntSerializeSize(uint64(len(tx.MsgTx.TxOut)))

	for i, input := range tx.Inputs {
		if len(input.LockingScript) == 0 {
			txin := tx.MsgTx.TxIn[i]
			if txin.UnlockingScript.IsFalseOpReturn() {
				protocols := channels.NewProtocols(unlocking_data.NewProtocol())
				msg, _, err := protocols.Parse(txin.UnlockingScript)
				if err == nil {
					unlockData, ok := msg.(*unlocking_data.UnlockingData)
					if ok {
						result += InputBaseSize + // outpoint + sequence
							VarIntSerializeSize(unlockData.Size) +
							int(unlockData.Size)
						continue
					}
				}
			}
		}

		size, err := InputSize(input.LockingScript)
		if err != nil {
			result += MaximumP2PKHInputSize // Fall back to P2PKH
			continue
		}

		result += size
	}

	for _, output := range tx.MsgTx.TxOut {
		result += output.SerializeSize()
	}

	return result
}

func (tx *TxBuilder) EstimatedFee() uint64 {
	return EstimatedFeeValue(uint64(tx.EstimatedSize()), float64(tx.FeeRate))
}

func (tx *TxBuilder) CalculateFee() error {
	_, err := tx.AdjustFee(int64(tx.EstimatedFee()) - tx.ActualFee())
	return err
}

func (tx *TxBuilder) ZeroizeFee() error {
	_, err := tx.AdjustFee(-tx.ActualFee())
	return err
}

// InputValue returns the sum of the values of the inputs.
func (tx *TxBuilder) InputValue() uint64 {
	inputValue := uint64(0)
	for i, input := range tx.Inputs {
		if input.Value == 0 {
			txin := tx.MsgTx.TxIn[i]
			if txin.UnlockingScript.IsFalseOpReturn() {
				protocols := channels.NewProtocols(unlocking_data.NewProtocol())
				msg, _, err := protocols.Parse(txin.UnlockingScript)
				if err == nil {
					unlockData, ok := msg.(*unlocking_data.UnlockingData)
					if ok {
						inputValue += unlockData.Value
						continue
					}
				}
			}
		}

		inputValue += input.Value
	}

	return inputValue
}

// OutputValue returns the sum of the values of the outputs.
func (tx *TxBuilder) OutputValue(includeChange bool) uint64 {
	outputValue := uint64(0)
	for i, output := range tx.MsgTx.TxOut {
		if includeChange || !tx.Outputs[i].IsRemainder {
			outputValue += uint64(output.Value)
		}
	}
	return outputValue
}

// Remainder returns the total value that we can get back by removing change.
func (tx *TxBuilder) Remainder() uint64 {
	value := uint64(0)
	for i, output := range tx.MsgTx.TxOut {
		if tx.Outputs[i].IsRemainder {
			value += EstimatedFeeValueDown(uint64(output.SerializeSize()), float64(tx.FeeRate))
			value += uint64(output.Value)
		}
	}
	return value
}

func (tx *TxBuilder) changeSum() uint64 {
	value := uint64(0)
	for i, output := range tx.MsgTx.TxOut {
		if tx.Outputs[i].IsRemainder {
			value += uint64(output.Value)
		}
	}
	return value
}

// adjustFee adjusts the tx fee up or down depending on if the amount is negative or positive.
// It returns true if no further fee adjustments should be attempted.
func (tx *TxBuilder) AdjustFee(amount int64) (bool, error) {
	if amount == int64(0) {
		return true, nil
	}

	done := false

	// Find change output
	changeOutputIndex := 0xffffffff
	for i, output := range tx.Outputs {
		if output.IsRemainder {
			changeOutputIndex = i
			break
		}
	}

	if amount > int64(0) {
		// Increase fee, transfer from change
		if changeOutputIndex == 0xffffffff {
			return false, errors.Wrap(ErrInsufficientValue, "No existing change for tx fee")
		}

		if tx.MsgTx.TxOut[changeOutputIndex].Value < uint64(amount) {
			return false, errors.Wrap(ErrInsufficientValue, "Not enough change for tx fee")
		}

		// Decrease change, thereby increasing the fee
		tx.MsgTx.TxOut[changeOutputIndex].Value -= uint64(amount)

		outputFee, inputFee, _ := OutputTotalCost(tx.MsgTx.TxOut[changeOutputIndex].LockingScript,
			tx.FeeRate)

		// Check if change is below dust
		if tx.MsgTx.TxOut[changeOutputIndex].Value < outputFee+inputFee {
			if !tx.Outputs[changeOutputIndex].addedForFee {
				// Don't remove outputs unless they were added by fee adjustment
				return false, errors.Wrap(ErrInsufficientValue, "Not enough change for tx fee")
			}

			// Remove change output since it is less than dust. Dust will go to miner.
			tx.MsgTx.TxOut = append(tx.MsgTx.TxOut[:changeOutputIndex],
				tx.MsgTx.TxOut[changeOutputIndex+1:]...)
			tx.Outputs = append(tx.Outputs[:changeOutputIndex], tx.Outputs[changeOutputIndex+1:]...)
			done = true
		}
	} else {
		// Decrease fee, transfer to change
		if changeOutputIndex == 0xffffffff {
			// Adjust amount of fee adjustment for new output being added.
			currentSize := uint64(tx.EstimatedSize())
			currentFee := EstimatedFeeValue(currentSize, float64(tx.FeeRate))

			var newSize uint64
			if len(tx.ChangeScript) == 0 {
				// Assume P2PKH for costs of adding an output so we don't fail out of this function
				// with an error if the remaining amount is too small to worry about.
				newSize = currentSize + P2PKHOutputSize
			} else {
				newSize = currentSize + uint64(OutputSize(tx.ChangeScript))
			}
			newFee := EstimatedFeeValue(newSize, float64(tx.FeeRate))

			changeOutputFee := newFee - currentFee

			if changeOutputFee > uint64(-amount) {
				return true, nil // adding a change output would make the adjustment negative
			}

			adjustment := uint64(-amount) - changeOutputFee

			// Add a change output if it would be more than the dust limit plus the fee to add the
			// output
			var outputFee, inputFee uint64
			if len(tx.ChangeScript) == 0 {
				// Assume P2PKH times two for costs of adding an output so we don't fail out of this
				// function with an error if the remaining amount is too small to worry about.
				outputFee = EstimatedFeeValue(P2PKHOutputSize*2, float64(tx.FeeRate))
				inputFee = EstimatedFeeValue(uint64(MaximumP2PKHInputSize*2), float64(tx.FeeRate))
			} else {
				outputFee, inputFee, _ = OutputTotalCost(tx.ChangeScript, tx.FeeRate)
			}
			if adjustment > outputFee+inputFee {
				if len(tx.ChangeScript) == 0 {
					return false, errors.Wrap(ErrChangeAddressNeeded, fmt.Sprintf("Remaining: %d",
						uint64(-amount)))
				}

				if err := tx.AddOutput(tx.ChangeScript, adjustment, true, false); err != nil {
					return false, err
				}

				tx.Outputs[len(tx.Outputs)-1].KeyID = tx.ChangeKeyID
				tx.Outputs[len(tx.Outputs)-1].addedForFee = true
			} else {
				// Leave less than dust as additional tx fee
				done = true
			}
		} else {
			// Increase change, thereby decreasing the fee
			// (amount is negative so subracting it increases the change value)
			tx.MsgTx.TxOut[changeOutputIndex].Value += uint64(-amount)
		}
	}

	return done, nil
}

// VarIntSerializeSize returns the number of bytes it would take to serialize
// val as a variable length integer.
func VarIntSerializeSize(val uint64) int {
	// The value is small enough to be represented by itself, so it's
	// just 1 byte.
	if val < 0xfd {
		return 1
	}

	// Discriminant 1 byte plus 2 bytes for the uint16.
	if val <= math.MaxUint16 {
		return 3
	}

	// Discriminant 1 byte plus 4 bytes for the uint32.
	if val <= math.MaxUint32 {
		return 5
	}

	// Discriminant 1 byte plus 8 bytes for the uint64.
	return 9
}

func EstimatedFeeValue(size uint64, feeRate float64) uint64 {
	return uint64(math.Ceil(float64(size) * feeRate))
}

func EstimatedFeeValueDown(size uint64, feeRate float64) uint64 {
	return uint64(math.Floor(float64(size) * feeRate))
}
