package forks

import "fmt"

type MantleForkName string

const (
	MantleBaseFee MantleForkName = "MantleBaseFee"
	MantleEverest MantleForkName = "MantleEverest"
	MantleEuboea  MantleForkName = "MantleEuboea"
	MantleSkadi   MantleForkName = "MantleSkadi"
	MantleLimb    MantleForkName = "MantleLimb"
	MantleArsia   MantleForkName = "MantleArsia"
	MantleElysium MantleForkName = "MantleElysium"
	MantleNone    MantleForkName = ""
	// This fork never existed, it is used to indicate that a optimism fork is not active at a given timestamp.
	MantleNoSupport MantleForkName = "MantleNoSupport"
)

var AllMantleForks = []MantleForkName{
	MantleBaseFee,
	MantleEverest,
	MantleEuboea,
	MantleSkadi,
	MantleLimb,
	MantleArsia,
	MantleElysium,
	// ADD NEW FORKS HERE!
}

func ForkToMantleFork(fork Name) MantleForkName {
	switch fork {
	case Interop:
		return MantleNoSupport
	case Canyon, Delta, Ecotone, Fjord, Granite, Holocene, Isthmus, Jovian:
		return MantleArsia
	case Bedrock, Regolith:
		return MantleSkadi
	default:
		return MantleNone
	}
}

var LatestMantleFork = AllMantleForks[len(AllMantleForks)-1]

func MantleForksFrom(fork MantleForkName) []MantleForkName {
	for i, f := range AllMantleForks {
		if f == fork {
			return AllMantleForks[i:]
		}
	}
	panic(fmt.Sprintf("invalid mantle fork: %s", fork))
}

var nextMantleFork = func() map[MantleForkName]MantleForkName {
	m := make(map[MantleForkName]MantleForkName, len(AllMantleForks))
	for i, f := range AllMantleForks {
		if i == len(AllMantleForks)-1 {
			m[f] = MantleNone
			break
		}
		m[f] = AllMantleForks[i+1]
	}
	return m
}()

func IsValidMantleFork(fork MantleForkName) bool {
	_, ok := nextMantleFork[fork]
	return ok
}
