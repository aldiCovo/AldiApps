package main

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// The LQ45 index as it really was over time, so a backtest only picks from the stocks that
// were in it on the day it decides. Using today's list for the past would favour the stocks
// that rose into it. Full lists are from Kontan, Liputan6 and MomsMoney, quoting IDX; the
// other periods are made from the stocks that came in and left, and universe_test.go checks
// they lead to the next full list.

type period struct {
	from    time.Time
	tickers []string
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, wib) }

func list(s string) []string { return strings.Fields(s) }

// change is a period made from the one before it.
type change struct {
	from     time.Time
	in, out  string
	fullList string // when the source also printed the whole list, to check against
}

var lq45History = buildHistory()

func buildHistory() []period {
	// February 2020, the earliest full list found.
	feb2020 := list(`ACES ADRO AKRA ANTM ASII BBCA BBNI BBRI BBTN BMRI BRPT BSDE BTPS CPIN CTRA
		ERAA EXCL GGRM HMSP ICBP INCO INDF INKP INTP ITMG JPFA JSMR KLBF LPPF MNCN PGAS PTBA PTPP
		PWON SCMA SMGR SRIL TBIG TKIM TLKM TOWR UNTR UNVR WIKA WSKT`)
	// Back from February 2020: it took in ACES, TBIG and TOWR for INDY, MEDC and TPIA, and
	// August 2019 took in BTPS, CTRA and JPFA for ADHI, ELSA and WSBP.
	aug2019 := apply(feb2020, "INDY MEDC TPIA", "ACES TBIG TOWR")
	feb2019 := apply(aug2019, "ADHI ELSA WSBP", "BTPS CTRA JPFA")

	periods := []period{
		{day(2019, 2, 1), feb2019},
		{day(2019, 8, 1), aug2019},
		{day(2020, 2, 3), feb2020},
	}
	for _, c := range lq45Changes {
		previous := periods[len(periods)-1].tickers
		periods = append(periods, period{c.from, apply(previous, c.in, c.out)})
	}
	return periods
}

// lq45Changes are the reviews from August 2020 on.
var lq45Changes = []change{
	{day(2020, 8, 3), "MDKA MIKA SMRA", "BRPT LPPF WSKT", `ACES ADRO AKRA ANTM ASII BBCA BBNI BBRI BBTN
			BMRI BSDE BTPS CPIN CTRA EXCL ERAA GGRM HMSP ICBP INCO INDF INKP INTP ITMG JPFA JSMR KLBF
			MDKA MIKA MNCN PGAS PTBA PTPP PWON SCMA SMGR SMRA SRIL TBIG TKIM TLKM TOWR UNTR UNVR WIKA`},
	{day(2021, 2, 1), "MEDC TPIA", "SCMA SRIL", `ACES ADRO AKRA ANTM ASII BBCA BBNI BBRI BBTN BMRI
			BSDE BTPS CPIN CTRA ERAA EXCL GGRM HMSP ICBP INCO INDF INKP INTP ITMG JPFA JSMR KLBF MDKA
			MEDC MIKA MNCN PGAS PTBA PTPP PWON SMGR SMRA TBIG TKIM TLKM TOWR TPIA UNTR UNVR WIKA`},
	{day(2021, 8, 2), "BRPT TINS", "BTPS CTRA", `ACES ADRO AKRA ANTM ASII BBCA BBNI BBRI BBTN BMRI
			BRPT BSDE CPIN ERAA EXCL GGRM HMSP ICBP INCO INDF INKP INTP ITMG JPFA JSMR KLBF MDKA MEDC
			MIKA MNCN PGAS PTBA PTPP PWON SMGR SMRA TBIG TINS TKIM TLKM TOWR TPIA UNTR UNVR WIKA`},
	// Bukalapak's fast entry.
	{day(2021, 9, 29), "BUKA", "SMRA", ""},
	{day(2022, 2, 2), "AMRT BFIN EMTK HRUM WSKT", "ACES AKRA BSDE JSMR PWON", `ADRO AMRT ANTM ASII
			BBCA BBNI BBRI BBTN BFIN BMRI BRPT BUKA CPIN EMTK ERAA EXCL GGRM HMSP HRUM ICBP INCO INDF
			INKP INTP ITMG JPFA KLBF MDKA MEDC MIKA MNCN PGAS PTBA PTPP SMGR TBIG TINS TKIM TLKM TOWR
			TPIA UNTR UNVR WIKA WSKT`},
	// GOTO came in and WSKT left between the February and August lists, GOTO by fast entry
	// after its April 2022 listing. The exact day isn't confirmed, so it counts from August.
	{day(2022, 8, 1), "ARTO BRIS INDY GOTO", "GGRM PTPP TKIM WSKT", `ADRO AMRT ANTM ARTO ASII BBCA
			BBNI BBRI BBTN BFIN BMRI BRIS BRPT BUKA CPIN EMTK ERAA EXCL GOTO HMSP HRUM ICBP INCO INDF
			INDY INKP INTP ITMG JPFA KLBF MDKA MEDC MIKA MNCN PGAS PTBA SMGR TBIG TINS TLKM TOWR TPIA
			UNTR UNVR WIKA`},
	{day(2023, 2, 1), "ACES AKRA ESSA SCMA SIDO SRTG", "BFIN ERAA HMSP MIKA MNCN WIKA", `ACES ADRO
			AKRA AMRT ANTM ARTO ASII BBCA BBNI BBRI BBTN BMRI BRIS BRPT BUKA CPIN EMTK ESSA EXCL GOTO
			HRUM ICBP INCO INDF INDY INKP INTP ITMG JPFA KLBF MDKA MEDC PGAS PTBA SCMA SIDO SMGR SRTG
			TBIG TINS TLKM TOWR TPIA UNTR UNVR`},
	{day(2023, 8, 1), "GGRM MAPI", "JPFA TINS", `ACES ADRO AKRA AMRT ANTM ARTO ASII BBCA BBNI BBRI
			BBTN BMRI BRIS BRPT BUKA CPIN EMTK ESSA EXCL GGRM GOTO HRUM ICBP INCO INDF INDY INKP INTP
			ITMG KLBF MAPI MDKA MEDC PGAS PTBA SCMA SIDO SMGR SRTG TBIG TLKM TPIA TOWR UNTR UNVR`},
	{day(2024, 2, 1), "MBMA MTEL PGEO PTMP", "INDY SCMA TBIG TPIA", ""},
	{day(2024, 5, 2), "AMMN ISAT", "EMTK PTMP", ""},
	{day(2024, 8, 1), "JSMR", "SRTG", ""},
	{day(2024, 11, 1), "ADMR SMRA", "GGRM HRUM", `ACES BBCA ESSA ITMG PGEO ADMR BBNI EXCL JSMR
			PTBA ADRO BBRI GOTO KLBF SIDO AKRA BBTN ICBP MAPI SMGR AMMN BMRI INCO MBMA SMRA AMRT BRIS
			INDF MDKA TLKM ANTM BRPT INKP MEDC TOWR ARTO BUKA INTP MTEL UNTR ASII CPIN ISAT PGAS UNVR`},
	{day(2025, 2, 3), "CTRA JPFA MAPA", "BUKA INTP MTEL", `ACES BRPT MAPA ADMR CPIN MAPI ADRO CTRA
			MBMA AKRA ESSA MDKA AMMN EXCL MEDC AMRT GOTO PGAS ANTM ICBP PGEO ARTO INCO PTBA ASII INDF
			SIDO BBCA INKP SMGR BBNI ISAT SMRA BBRI ITMG TLKM BBTN JPFA TOWR BMRI JSMR UNTR BRIS KLBF
			UNVR`},
	// May 2025 changed nothing.
	{day(2025, 8, 1), "AADI SCMA", "ESSA SIDO", `AADI ACES ADMR ADRO AKRA AMMN AMRT ANTM ARTO ASII
			BBCA BBNI BBRI BBTN BMRI BRIS BRPT CPIN CTRA EXCL GOTO ICBP INCO INDF INKP ISAT ITMG JPFA
			JSMR KLBF MAPA MAPI MBMA MDKA MEDC PGAS PGEO PTBA SCMA SMGR SMRA TLKM TOWR UNTR UNVR`},
	{day(2025, 11, 3), "BUMI DSSA EMTK HEAL NCKL", "ARTO BRIS JSMR MAPA SMRA", `AADI ACES ADMR ADRO
			AKRA AMMN AMRT ANTM ASII BBCA BBNI BBRI BBTN BMRI BRPT BUMI CPIN CTRA DSSA EMTK EXCL GOTO
			HEAL ICBP INCO INDF INKP ISAT ITMG JPFA KLBF MAPI MBMA MDKA MEDC NCKL PGAS PGEO PTBA SCMA
			SMGR TLKM TOWR UNTR UNVR`},
	{day(2026, 2, 2), "BREN", "ACES", ""},
	{day(2026, 5, 4), "CUAN DEWA ESSA HRTA WIFI", "BREN CTRA DSSA HEAL NCKL", `AADI ADMR ADRO AKRA
			AMMN AMRT ANTM ASII BBCA BBNI BBRI BBTN BMRI CUAN BRPT BUMI CPIN DEWA EMTK ESSA EXCL GOTO
			HRTA ICBP INCO INDF INKP ISAT ITMG JPFA KLBF MAPI MBMA MDKA MEDC PGAS PGEO PTBA SCMA SMGR
			TLKM TOWR UNTR UNVR WIFI`},
	{day(2026, 8, 3), "INDY NCKL", "SMGR TOWR", `AADI ADMR ADRO AKRA AMMN AMRT ANTM ASII BBCA BBNI
			BBRI BBTN BMRI BRPT BUMI CPIN CUAN DEWA EMTK ESSA EXCL GOTO HRTA ICBP INCO INDF INDY INKP
			ISAT ITMG JPFA KLBF MAPI MBMA MDKA MEDC NCKL PGAS PGEO PTBA SCMA TLKM UNTR UNVR WIFI`},
	{day(2026, 11, 2), "SMGR", "SCMA", "AADI ADMR ADRO AKRA AMMN AMRT ANTM ASII BBCA BBNI BBRI BBTN BMRI BRPT BUMI CPIN CUAN DEWA EMTK ESSA EXCL GOTO HRTA ICBP INCO INDF INDY INKP ISAT ITMG JPFA KLBF MAPI MBMA MDKA MEDC NCKL PGAS PGEO PTBA SMGR TLKM UNTR UNVR WIFI"},
}

// apply takes a list, adds the stocks in and removes the stocks out, and panics when that
// doesn't make sense, since the history is fixed data.
func apply(tickers []string, in, out string) []string {
	result := slices.Clone(tickers)
	for _, t := range list(out) {
		i := slices.Index(result, t)
		if i < 0 {
			panic(fmt.Sprintf("LQ45 history: %s leaves but isn't in the list", t))
		}
		result = slices.Delete(result, i, i+1)
	}
	for _, t := range list(in) {
		if slices.Contains(result, t) {
			panic(fmt.Sprintf("LQ45 history: %s comes in but is already in the list", t))
		}
		result = append(result, t)
	}
	slices.Sort(result)
	return result
}

// lq45On returns the stocks in LQ45 on day d, or nil before the history starts.
func lq45On(d time.Time) []string {
	var current []string
	for _, p := range lq45History {
		if p.from.After(d) {
			break
		}
		current = p.tickers
	}
	return current
}

// everLQ45 is every stock that was in LQ45 at some point of the history.
func everLQ45() []string {
	var all []string
	for _, p := range lq45History {
		for _, t := range p.tickers {
			if !slices.Contains(all, t) {
				all = append(all, t)
			}
		}
	}
	slices.Sort(all)
	return all
}
