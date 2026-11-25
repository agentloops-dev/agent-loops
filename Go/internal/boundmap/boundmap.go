// Package boundmap holds a map of totals to a count of keys and a largest key, and loses no
// total: past them, an amount goes on the key Other (#7245).
package boundmap

// Other is the key that has what the other keys have no room for.
const Other = "other"

// Fits says that m has room for key: it is there, or it is at most bytes long and, with Other,
// m stays within most keys.
func Fits[V any](m map[string]V, key string, most, bytes int) bool {
	if len(key) > bytes {
		return false
	}
	_, there := m[key]
	return there || key == Other || len(m) < most-1
}

// Key is key when m has room for it, else Other.
func Key[V any](m map[string]V, key string, most, bytes int) string {
	if Fits(m, key, most, bytes) {
		return key
	}
	return Other
}

const (
	// MostCurrencies and currencyBytes bound a cost by currency. A runtime says the currency.
	MostCurrencies = 16
	currencyBytes  = 16
)

// AddMoney adds an amount to a cost by currency.
func AddMoney(m map[string]float64, currency string, amount float64) {
	m[Key(m, currency, MostCurrencies, currencyBytes)] += amount
}
