package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"
)

// demoLine is one line of the fictional demo household. Amount is in whole
// kroner per month it applies to; growth is the yearly rise in per mille.
type demoLine struct {
	section, en, da string
	amount          int64
	months          []int // nil means every month
	growth          int64
}

var quarterly = []int{1, 4, 7, 10}

var demoLines = []demoLine{
	{"income", "Salary Alex", "Løn Alex", 32000, nil, 35},
	{"income", "Salary Sam", "Løn Sam", 27500, nil, 35},
	{"income", "Child benefit", "Børneydelse", 4600, quarterly, 20},
	{"income", "Holiday allowance", "Feriepenge", 6000, []int{5}, 30},

	{"expense", "Mortgage", "Boliglån", 9800, nil, 0},
	{"expense", "Property tax", "Ejendomsskat", 7200, []int{1, 7}, 40},
	{"expense", "Heating", "Varme", 1100, nil, 50},
	{"expense", "Electricity", "El", 650, nil, 60},
	{"expense", "Water", "Vand", 1500, quarterly, 40},
	{"expense", "Home insurance", "Husforsikring", 1650, quarterly, 30},
	{"expense", "Car loan", "Billån", 2900, nil, 0},
	{"expense", "Car insurance", "Bilforsikring", 7800, []int{1}, 30},
	{"expense", "Fuel", "Brændstof", 1400, nil, 30},
	{"expense", "Internet", "Internet", 299, nil, 0},
	{"expense", "Mobile phones", "Mobiltelefoner", 298, nil, 0},
	{"expense", "Streaming", "Streaming", 239, nil, 80},
	{"expense", "Childcare", "Institution", 3400, []int{1, 2, 3, 4, 5, 6, 8, 9, 10, 11, 12}, 40},
	{"expense", "Union and unemployment fund", "Fagforening og a-kasse", 1050, nil, 20},
	{"expense", "Gym", "Fitness", 598, nil, 0},

	{"allocation", "Food account", "Madkonto", 10500, nil, 40},
	{"allocation", "Pocket money Alex", "Lommepenge Alex", 4000, nil, 0},
	{"allocation", "Pocket money Sam", "Lommepenge Sam", 4000, nil, 0},
	{"allocation", "Savings", "Opsparing", 9000, nil, 50},
	{"allocation", "Holiday savings", "Ferieopsparing", 3500, nil, 0},
	{"allocation", "Buffer account", "Bufferkonto", 4000, nil, 30},
	{"allocation", "Child savings", "Børneopsparing", 750, nil, 0},
}

// runDemo is the "ledger demo" command.
func runDemo(dsn, lang string) {
	db, err := openDB(dsn)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	if err := migrateAll(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := seedDemo(db, lang); err != nil {
		log.Fatalf("demo: %v", err)
	}
	log.Printf("added demo budgets for %d–%d", time.Now().Year()-2, time.Now().Year())
}

// seedDemo fills an empty database with three years of the demo household,
// ending with the current year. It never touches a database with budgets.
func seedDemo(db *sql.DB, lang string) error {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM budget_years").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errors.New("the database already has budgets; demo data is only added to an empty database")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	budget, err := defaultBudget(context.Background(), tx)
	if err != nil {
		return err
	}

	first := time.Now().Year() - 2
	for i := int64(0); i < 3; i++ {
		res, err := tx.Exec("INSERT INTO budget_years (budget_id, year) VALUES (?, ?)", budget, first+int(i))
		if err != nil {
			return err
		}
		yearID, _ := res.LastInsertId()
		for sort, l := range demoLines {
			name := l.en
			if lang == "da" {
				name = l.da
			}
			res, err := tx.Exec("INSERT INTO budget_lines (year_id, section, name, sort) VALUES (?, ?, ?, ?)",
				yearID, l.section, name, sort)
			if err != nil {
				return err
			}
			lineID, _ := res.LastInsertId()
			kr := (l.amount*(1000+l.growth*i) + 500) / 1000 // grown and rounded to whole kroner
			ms := l.months
			if ms == nil {
				ms = months
			}
			for _, m := range ms {
				if _, err := tx.Exec("INSERT INTO budget_amounts (line_id, month, amount) VALUES (?, ?, ?)",
					lineID, m, Money(kr*100)); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}
