package liqmap

const (
	DefaultDBPath     = defaultDBPath
	DefaultServerAddr = defaultServerAddr
	DefaultSymbol     = defaultSymbol
)

func Getenv(key, fallback string) string {
	return getenv(key, fallback)
}

func SetupLogging(debug bool) (func(), error) {
	return setupLogging(debug)
}
