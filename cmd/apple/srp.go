package main

import (
	"context"
	"math/big"
	"time"

	"github.com/donnel666/remail/internal/appleweb"
)

var (
	srpN = appleweb.GroupN()
	srpG = appleweb.GroupG()
)

func srpPublicA() (*big.Int, []byte, error) {
	return appleweb.SRPPublicA()
}

func srpProofs(identity, password string, salt []byte, iterations int, protocol string, privateA *big.Int, publicA, serverB []byte) ([]byte, []byte, error) {
	return appleweb.SRPProofs(identity, password, salt, iterations, protocol, privateA, publicA, serverB)
}

func solveHashcash(ctx context.Context, challenge string, bits int) (string, error) {
	return appleweb.SolveHashcash(ctx, challenge, bits)
}

func makeFrameID() (string, error) {
	return appleweb.FrameID()
}

func fdClientInfo(now time.Time) (string, error) {
	return appleweb.FDClientInfo(now)
}

func padN(value []byte) []byte {
	return appleweb.PadN(value)
}

func encodeAppleCollector(raw string) (string, error) {
	return appleweb.EncodeCollector(raw)
}
