package txbuilder

import (
	"bytes"
	"fmt"

	"github.com/tokenized/channels"
	"github.com/tokenized/channels/unlocking_data"
	"github.com/tokenized/pkg/expanded_tx"
	"github.com/tokenized/pkg/wire"
)

func TxString(tx expanded_tx.TransactionWithOutputs) string {
	result := &bytes.Buffer{}
	result.Write([]byte(fmt.Sprintf("TxID : %s\n", tx.TxID())))

	inputCount := tx.InputCount()
	inputsValue := uint64(0)
	inputsSize := 0
	feeSizeKnown := true
	feeValueKnown := true
	result.Write([]byte(fmt.Sprintf("  Inputs %d:\n", inputCount)))
	for inputIndex := 0; inputIndex < inputCount; inputIndex++ {
		txin := tx.Input(inputIndex)
		result.Write([]byte(fmt.Sprintf("    Outpoint: %s:%d\n", txin.PreviousOutPoint.Hash,
			txin.PreviousOutPoint.Index)))
		result.Write([]byte(fmt.Sprintf("    Unlocking script: %s\n", txin.UnlockingScript)))
		result.Write([]byte(fmt.Sprintf("    Sequence: 0x%08x\n", txin.Sequence)))

		if txout, err := tx.InputOutput(inputIndex); err == nil {
			inputsValue += txout.Value

			unlockingSize, err := UnlockingScriptSize(txout.LockingScript)
			if err == nil {
				result.Write([]byte(fmt.Sprintf("    Estimated unlocking script size: %d\n",
					unlockingSize)))
				inputsSize += InputBaseSize + wire.VarIntSerializeSize(uint64(unlockingSize)) +
					unlockingSize
			} else {
				feeSizeKnown = false
				inputsSize += txin.SerializeSize()
			}

			result.Write([]byte(fmt.Sprintf("    Locking script: %s\n", txout.LockingScript)))
			result.Write([]byte(fmt.Sprintf("    Value: %d\n\n", txout.Value)))
			continue
		}

		protocols := channels.NewProtocols(unlocking_data.NewProtocol())
		if msg, _, err := protocols.Parse(txin.UnlockingScript); err == nil {
			if unlockData, ok := msg.(*unlocking_data.UnlockingData); ok {
				result.Write([]byte("    Embedded unlocking data:\n"))

				if unlockData.Size > 0 {
					result.Write([]byte(fmt.Sprintf("      Size: %d\n", unlockData.Size)))
				} else {
					feeSizeKnown = false
				}

				if unlockData.Value > 0 {
					result.Write([]byte(fmt.Sprintf("      Value: %d\n", unlockData.Value)))
				} else {
					feeValueKnown = false
				}

				result.Write([]byte(fmt.Sprintf("      Party: %s\n\n", unlockData.Party)))

				inputsValue += unlockData.Value
				inputsSize += InputBaseSize + wire.VarIntSerializeSize(uint64(unlockData.Size)) +
					len(txin.UnlockingScript)
				continue
			}
		}

		if len(txin.UnlockingScript) > 0 && !txin.UnlockingScript.IsFalseOpReturn() {
			result.Write([]byte("\n"))
			inputsSize += txin.SerializeSize()
			feeValueKnown = false
			continue
		}

		result.Write([]byte("    Input's output not found\n\n"))
		feeSizeKnown = false
		feeValueKnown = false
		inputsSize += txin.SerializeSize()
	}

	outputCount := tx.OutputCount()
	outputsValue := uint64(0)
	outputsSize := 0
	result.Write([]byte(fmt.Sprintf("  Outputs %d:\n", outputCount)))
	for outputIndex := 0; outputIndex < outputCount; outputIndex++ {
		txout := tx.Output(outputIndex)
		outputsSize += txout.SerializeSize()
		result.Write([]byte(fmt.Sprintf("    Locking script: %s\n", txout.LockingScript)))
		result.Write([]byte(fmt.Sprintf("    Value: %d\n\n", txout.Value)))

		outputsValue += txout.Value
	}

	if msgTx := tx.GetMsgTx(); msgTx != nil {
		result.Write([]byte(fmt.Sprintf("  Locktime: %d\n\n", msgTx.LockTime)))
		result.Write([]byte(fmt.Sprintf("  Raw size: %d bytes\n", msgTx.SerializeSize())))
	}

	var txSize int
	if feeSizeKnown {
		txSize = BaseTxSize + wire.VarIntSerializeSize(uint64(inputCount)) + inputsSize +
			wire.VarIntSerializeSize(uint64(outputCount)) + outputsSize
		result.Write([]byte(fmt.Sprintf("  Estimated tx size: %d bytes\n", txSize)))
	}

	if feeValueKnown {
		if inputsValue >= outputsValue {
			result.Write([]byte(fmt.Sprintf("  Fee: %d (inputs %d, outputs %d)\n",
				inputsValue-outputsValue, inputsValue, outputsValue)))

			if feeSizeKnown {
				result.Write([]byte(fmt.Sprintf("  Fee rate: %0.04f sat/byte\n",
					float32(inputsValue-outputsValue)/float32(txSize))))
			}
		} else {
			result.Write([]byte(fmt.Sprintf("  Negative fee: -%d (inputs %d, outputs %d)\n",
				outputsValue-inputsValue, inputsValue, outputsValue)))
		}
	}

	return string(result.Bytes())
}
