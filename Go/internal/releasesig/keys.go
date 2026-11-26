package releasesig

// The release's public keys: the one release.yml signs with, and the spare, whose private
// half stays offline until the current one is lost or replaced. install.sh holds the same
// two (a test keeps them equal).
//
// While they are the test keys, release.yml stops at its first job (CheckRelease), and a
// machine trusts no release but one that its AGENTS_RELEASE_KEY signed.
const (
	CurrentKey = "MCowBQYDK2VwAyEAbK/TkG4B9kEq3EoU3wxL+UOmjOjCTaVMqvJQ6xIryhg="
	SpareKey   = "MCowBQYDK2VwAyEAW3GLYtpZB4egkA88jeiabbtP9ynkRMhM/1sU5w4vP60="
)

// The keys the tests sign with. Their private halves are in testdata, so only a Go test
// trusts them.
const (
	TestCurrentKey = "MCowBQYDK2VwAyEAg+yKeWGty0eECexXiUzhZYho1S9rg7gWaFv/joOAlM0="
	TestSpareKey   = "MCowBQYDK2VwAyEAZmXkulKLAIRvSOspl/hNtxAh6nRfFJGO5C08/B79qXE="
)
