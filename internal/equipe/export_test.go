package equipe

// IterationsPourTests abaisse le coût de PBKDF2 pendant un test, pour que les
// nombreuses tentatives de connexion restent rapides (aussi avec -race).
func IterationsPourTests(n int) (restaurer func()) {
	ancien := iterationsPBKDF2
	iterationsPBKDF2 = n
	return func() { iterationsPBKDF2 = ancien }
}
