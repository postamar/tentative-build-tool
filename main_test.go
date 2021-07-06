package main

import (
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"strings"
	"testing"
	"time"
)

const testDataDir = "testdata"
const inputSuffix = ".in.yaml"
const outputSuffix = ".out.yaml"

// TestDataDriven runs all the test cases encoded as yaml files in the testdata
// subdirectory.
// The input file is parsed into a TestCaseInput, see that type definition for
// more details.
// The output file encodes the expected state of the TestCaseOutput at the end of
// the run, again see that type definition for more details.
func TestDataDriven(t *testing.T) {
	finfo, err := ioutil.ReadDir(testDataDir)
	require.NoError(t, err)
	for _, f := range finfo {
		if !strings.HasSuffix(f.Name(), inputSuffix) {
			continue
		}
		caseName := f.Name()[:len(f.Name())-len(inputSuffix)]

		// Run a data-driven test case.
		t.Run(caseName, func(t *testing.T) {
			input, err := ioutil.ReadFile(testDataDir + "/" + caseName + inputSuffix)
			require.NoError(t, err)
			expectedOutput, err := ioutil.ReadFile(testDataDir + "/" + caseName + outputSuffix)
			require.NoError(t, err)

			var tci TestCaseInput
			err = yaml.Unmarshal(input, &tci)
			require.NoError(t, err)

			c := tci.NewTestGithubClient(t)
			const fakeDuration = time.Second
			StateMachine(&c, fakeDuration)

			actualOutput, err := yaml.Marshal(c.ToTestCaseOutput())
			require.NoError(t, err)
			require.Equal(t, string(expectedOutput), string(actualOutput))
		})
	}
}
