package chain

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"les-miserables-chain/database"
	"les-miserables-chain/utils"
	"log"
	"math/big"
	"strconv"

	"github.com/boltdb/bolt"
)

//链结构体
type Chain struct {
	LastHash []byte   //链的最新高度区块hash
	DB       *bolt.DB //数据库对象
}

//创世区块链
func InitBlockChain(address string) *Chain {
	//根据nodeID生成数据库名

	fmt.Printf("数据库文件生成成功，存储位置为%s\n", database.DbFile)

	var lastHash []byte

	db, err := bolt.Open(database.DbFile, 0600, nil)
	if err != nil {
		log.Panic(err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(database.BlockBucket))
		//判断bucket是否存在
		if b == nil {
			fmt.Println("Creating the genesis block.....")
			//创世区块集成交易
			coinbaseTx := NewCoinBaseTX(address)
			genesisBlock := NewGenesisBlock(coinbaseTx)
			//bucket不存在，创建一个桶
			b, err := tx.CreateBucket([]byte(database.BlockBucket))
			if err != nil {
				log.Panic(err)
			}
			//创世区块存储到bucket中
			err = b.Put(genesisBlock.BlockCurrentHash, Serialize(genesisBlock))
			if err != nil {
				log.Panic(err)
			}
			//存储最新的出块hash
			err = b.Put([]byte("last"), genesisBlock.BlockCurrentHash)
			if err != nil {
				log.Panic(err)
			}
			lastHash = genesisBlock.BlockCurrentHash
		} else {
			fmt.Println("请勿重复初始化区块链!")
			lastHash = b.Get([]byte("last"))
		}
		return nil
	})
	if err != nil {
		log.Panic(err)
	}
	return &Chain{
		LastHash: lastHash,
		DB:       db,
	}
}

//返回Blockchain对象
func BlockchainObject() *Chain {

	db, err := bolt.Open(database.DbFile, 0600, nil)
	if err != nil {
		log.Fatal(err)
	}

	var tip []byte

	err = db.View(func(tx *bolt.Tx) error {

		b := tx.Bucket([]byte(database.BlockBucket))

		if b != nil {
			// 读取最新区块的Hash
			tip = b.Get([]byte("last"))

		}

		return nil
	})

	return &Chain{tip, db}
}

//查询地址下的未花费交易集合-已作废
func (chain *Chain) FindUnspentTransactions(address string) []Transaction {
	//未花费交易
	var unspentTxs []Transaction
	//存储花费的交易
	spentTxs := make(map[string][]int)
	blockchainIterator := chain.Iterator()
	var hashInt big.Int

	//迭代遍历区块链
	for {
		err := blockchainIterator.DB.View(func(tx *bolt.Tx) error {
			//获取最新区块
			b := tx.Bucket([]byte(database.BlockBucket))
			blockBytes := b.Get(blockchainIterator.CurrentHash)
			block := DeserializeBlock(blockBytes)
			//遍历区块交易信息
			for _, transaction := range block.Transactions {
				//将交易ID转换为16进制
				index := hex.EncodeToString(transaction.TxHash)
				//Outputs的label
			Outputs:
				//遍历交易输出
				for outIdx, out := range transaction.TxOutputs {
					//判断是否已经被花费？
					if spentTxs[index] != nil {
						//遍历花费交易
						for _, spentOut := range spentTxs[index] {
							if spentOut == outIdx {
								continue Outputs
							}
						}
					}
					//如果是交易输出的解锁对象，则加入未花费交易
					if out.UnLockScriptPubKeyWithAddress(address) {
						unspentTxs = append(unspentTxs, *transaction)
					}
				}
				//判断是否是coinbase交易
				if transaction.IsCoinbase() == false {
					//遍历交易输入
					for _, in := range transaction.TxInputs {
						//如果是交易输入解锁对象，则加入已花费交易中
						publicKeyHash := utils.Base58Decode([]byte(address))
						ripemd160Hash := publicKeyHash[1 : len(publicKeyHash)-4]
						if in.UnlockPublicKeyHash(ripemd160Hash) {
							//inTxID := hex.EncodeToString(in.TxID)
							spentTxs[index] = append(spentTxs[index], in.OutputIndex)
						}
					}
				}

			}
			fmt.Println()
			return nil
		})
		if err != nil {
			log.Panic(err)
		}
		blockchainIterator = blockchainIterator.Next()
		hashInt.SetBytes(blockchainIterator.CurrentHash)
		if hashInt.Cmp(big.NewInt(0)) == 0 {
			break
		}
	}
	return unspentTxs
}

//查询可用的未花费输出信息-已作废
func (chain *Chain) FindSpendableOutputs(address string, amount int) (int, map[string][]int) {
	//未花费交易输出
	unspentOutputs := make(map[string][]int)
	//未花费交易
	unspentTxs := chain.FindUnspentTransactions(address)
	//未花费交易输出的value总量
	accumulated := 0
Work:
	//遍历未花费交易
	for _, tx := range unspentTxs {
		//获取未花费交易的交易ID
		txh := hex.EncodeToString(tx.TxHash)
		//遍历该未花费交易下的未花费交易输出
		for outIdx, out := range tx.TxOutputs {
			if out.UnLockScriptPubKeyWithAddress(address) && accumulated < amount {
				accumulated += out.Value
				unspentOutputs[txh] = append(unspentOutputs[txh], outIdx)
				if accumulated >= amount {
					break Work
				}
			}
		}
	}
	return accumulated, unspentOutputs
}

//获取地址余额(低数据量)
func (chain *Chain) GetBalance(address string) int {
	utxos := chain.UnUTXOs(address, []*Transaction{})
	var amount int
	for _, utxo := range utxos {
		amount = amount + utxo.OutPut.Value
	}
	return amount
}

//区块派生
func (chain *Chain) MineBlock(from []string, to []string, amount []string) error {
	utxoRecord := &UTXORecord{chain}
	var txs []*Transaction
	//1.遍历多方转账，创建交易
	for index, address := range from {
		value, _ := strconv.Atoi(amount[index])
		tx := CreateTransaction(address, to[index], value, utxoRecord, txs)
		//levy page 4 {}
		txs = append(txs, tx)
		//fmt.Println(tx)
	}
	//增加交易奖励
	tx := NewCoinBaseTX(from[0])
	txs = append(txs, tx)

	var block *Block
	//2.获取最新高度的区块
	err := chain.DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(database.BlockBucket))
		if b != nil {
			hash := b.Get([]byte("last"))
			blockBytes := b.Get(hash)
			block = DeserializeBlock(blockBytes)
		}
		return nil
	})
	if err != nil {
		return err
	}
	//区块签名验证
	var txs0 []*Transaction
	for _, tx := range txs {
		if chain.VerifyTransaction(tx, txs0) != true {
			log.Panic("Invalid transaction.")
		}
		txs0 = append(txs0, tx)
	}
	//3.根据当前区块构建新的区块
	block = NewBlock(txs, block.Height, block.BlockCurrentHash)

	err = chain.DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(database.BlockBucket))
		if b != nil {
			_ = b.Put(block.BlockCurrentHash, Serialize(block))
			_ = b.Put([]byte("last"), block.BlockCurrentHash)
			chain.LastHash = block.BlockCurrentHash
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

//交易签名
func (chain *Chain) SignTransaction(tx *Transaction, privateKey ecdsa.PrivateKey, txs []*Transaction) {
	//如果是创世交易，则不进行签名
	if tx.IsCoinbase() {
		return
	}
	prevTxs := make(map[string]Transaction)
	for _, in := range tx.TxInputs {
		prevTx, err := chain.FindTransaction(in.TxID, txs)
		if err != nil {
			log.Panic(err)
		}
		prevTxs[hex.EncodeToString(prevTx.TxHash)] = prevTx
	}
	tx.Sign(privateKey, prevTxs)
}

//查找旧交易
func (chain *Chain) FindTransaction(ID []byte, txs []*Transaction) (Transaction, error) {
	for _, tx := range txs {
		if bytes.Compare(tx.TxHash, ID) == 0 {
			return *tx, nil
		}
	}

	bci := chain.Iterator()
	for {
		block := bci.NextBlock()
		for _, tx := range block.Transactions {
			if bytes.Compare(tx.TxHash, ID) == 0 {
				return *tx, nil
			}
		}
		var hashInt big.Int
		hashInt.SetBytes(block.BlockPreHash)

		if big.NewInt(0).Cmp(&hashInt) == 0 {
			break
		}
	}
	return Transaction{}, nil
}

func (chain *Chain) VerifyTransaction(tx *Transaction, txs []*Transaction) bool {
	prevTxs := make(map[string]Transaction)

	for _, vin := range tx.TxInputs {
		prevTx, err := chain.FindTransaction(vin.TxID, txs)
		if err != nil {
			log.Panic(err)
		}
		prevTxs[hex.EncodeToString(prevTx.TxHash)] = prevTx
	}
	return tx.Verify(prevTxs)
}

//查找UTXO的map
func (chain *Chain) FindUTXOMap() map[string]*TXOutputs {
	bcIterator := chain.Iterator()

	//已花费UTXO
	spentUTXOMap := make(map[string][]*TXInput)

	utxoMaps := make(map[string]*TXOutputs)

	for {
		//从当前区块开始遍历
		block := bcIterator.NextBlock()
		for i := len(block.Transactions) - 1; i >= 0; i-- {
			txOutputs := &TXOutputs{[]*UTXO{}}
			tx := block.Transactions[i]
			if tx.IsCoinbase() == false {
				for _, vin := range tx.TxInputs {
					txHash := hex.EncodeToString(vin.TxID)
					spentUTXOMap[txHash] = append(spentUTXOMap[txHash], vin)
				}
			}
			txHash := hex.EncodeToString(tx.TxHash)

		Work:
			for index, out := range tx.TxOutputs {
				txInputs := spentUTXOMap[txHash]
				if len(txInputs) > 0 {
					isSpent := false
					for _, in := range txInputs {
						outPublicKey := out.ScriptPubKey
						inPublicKey := in.PublicKey
						if bytes.Compare(outPublicKey, utils.GetRipemd160(inPublicKey)) == 0 {
							if index == in.OutputIndex {
								isSpent = true
								continue Work
							}
						}
					}
					if isSpent == false {
						utxo := &UTXO{tx.TxHash, index, out}
						txOutputs.UTXOS = append(txOutputs.UTXOS, utxo)
					}
				} else {
					utxo := &UTXO{tx.TxHash, index, out}
					txOutputs.UTXOS = append(txOutputs.UTXOS, utxo)
				}
			}
			utxoMaps[txHash] = txOutputs
		}
		var hashInt big.Int
		hashInt.SetBytes(block.BlockPreHash)
		if hashInt.Cmp(big.NewInt(0)) == 0 {
			break
		}
	}
	return utxoMaps

}
