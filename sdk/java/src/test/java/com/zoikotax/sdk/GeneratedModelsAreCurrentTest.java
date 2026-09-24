// The drift gate.
//
// The generator runs on every build and writes into target/; this compares
// what it wrote with the committed models in src/, in both directions. A
// contract change this SDK has not been regenerated for is then a build
// failure rather than a discovery (ADR-0010 §2.1, §5.1 control 1).
//
// It is a test rather than `git diff --exit-code` so that it also catches a
// file the generator now produces and nobody has added — git does not diff an
// untracked file — and so that it runs anywhere `mvn verify` runs, with or
// without a checkout.

package com.zoikotax.sdk;

import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.TreeSet;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;

class GeneratedModelsAreCurrentTest {

  private static Path dir(String property) {
    String value = System.getProperty(property);
    if (value == null) {
      fail("system property " + property + " is not set; run this through mvn verify");
    }
    return Path.of(value);
  }

  private static Set<String> files(Path root) throws IOException {
    try (Stream<Path> walk = Files.walk(root)) {
      return walk.filter(Files::isRegularFile)
          .map(p -> root.relativize(p).toString().replace('\\', '/'))
          .collect(Collectors.toCollection(TreeSet::new));
    }
  }

  /** Line endings are a checkout setting, not a difference in the models. */
  private static String read(Path file) throws IOException {
    return Files.readString(file, StandardCharsets.UTF_8).replace("\r\n", "\n");
  }

  @Test
  void theCommittedModelsAreWhatTheContractGenerates() throws IOException {
    Path generated = dir("zoikotax.generated");
    Path committed = dir("zoikotax.committed");
    assertTrue(Files.isDirectory(generated), "the generator did not run: " + generated);

    Set<String> want = files(generated);
    Set<String> have = Files.isDirectory(committed) ? files(committed) : Set.of();
    List<String> drift = new ArrayList<>();

    for (String name : want) {
      if (!have.contains(name)) {
        drift.add("missing: " + name);
      } else if (!read(generated.resolve(name)).equals(read(committed.resolve(name)))) {
        drift.add("differs: " + name);
      }
    }
    for (String name : have) {
      if (!want.contains(name)) {
        drift.add("no longer generated: " + name);
      }
    }

    if (!drift.isEmpty()) {
      fail(
          "The committed models do not match the contract. Run\n\n"
              + "    mvn -B -Pregenerate process-sources\n\n"
              + "and commit the result. Drift:\n  "
              + String.join("\n  ", drift));
    }
  }
}
