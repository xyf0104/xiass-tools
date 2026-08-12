//! Evaluation of npm `engines.node` ranges.
//!
//! npm's range syntax is not Cargo's, so `semver::VersionReq` cannot be reused:
//! npm separates alternatives with `||` and treats whitespace as AND, while
//! Cargo has no OR at all. The distinction matters because these ranges are
//! routinely disjoint. `>=22.22.3 <23 || >=24.15.0 <25 || >=25.9.0` accepts
//! 22.22.3 and 24.15.0 but rejects 24.13.0, which a plain "at least the lowest
//! version" comparison would wrongly wave through.

/// Whether a version satisfies a range.
///
/// `Unknown` exists so an unparseable range never blocks an install: npm itself
/// only warns about engine mismatches, so refusing to act on syntax this module
/// does not understand would be a worse failure than proceeding.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EngineSupport {
    Satisfied,
    Unsatisfied,
    Unknown,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
struct NodeVersion([u64; 3]);

/// Parses `v24.13.0`, `24.13`, or `24` into comparable parts.
///
/// Missing components read as zero, and any prerelease or build suffix is
/// dropped: `engines.node` ranges in the wild do not discriminate on them.
fn parse_version(value: &str) -> Option<NodeVersion> {
    let value = value.trim().trim_start_matches(['v', 'V', '=']).trim();
    let numeric: String = value
        .chars()
        .take_while(|character| character.is_ascii_digit() || *character == '.')
        .collect();
    let mut parts = [0_u64; 3];
    let mut seen = 0;
    for (index, part) in numeric.trim_matches('.').split('.').enumerate() {
        if index >= 3 {
            break;
        }
        parts[index] = part.parse().ok()?;
        seen += 1;
    }
    (seen > 0).then_some(NodeVersion(parts))
}

/// How many components the range author actually wrote, which decides what a
/// bare or caret comparator expands to: `22` covers all of 22.x, `22.1` covers
/// all of 22.1.x, and `22.1.2` covers exactly one release.
fn version_precision(value: &str) -> usize {
    value
        .trim()
        .trim_start_matches(['v', 'V', '='])
        .trim()
        .chars()
        .take_while(|character| character.is_ascii_digit() || *character == '.')
        .collect::<String>()
        .trim_matches('.')
        .split('.')
        .count()
        .min(3)
}

fn next_at(version: NodeVersion, precision: usize) -> NodeVersion {
    let mut parts = version.0;
    match precision {
        1 => {
            parts[0] += 1;
            parts[1] = 0;
            parts[2] = 0;
        }
        2 => {
            parts[1] += 1;
            parts[2] = 0;
        }
        _ => parts[2] += 1,
    }
    NodeVersion(parts)
}

/// A single comparator reduced to a half-open interval `[low, high)`.
#[derive(Debug, Clone, Copy)]
struct Bound {
    low: Option<NodeVersion>,
    low_inclusive: bool,
    high: Option<NodeVersion>,
}

impl Bound {
    fn admits(&self, version: NodeVersion) -> bool {
        let above_low = match self.low {
            Some(low) if self.low_inclusive => version >= low,
            Some(low) => version > low,
            None => true,
        };
        let below_high = self.high.is_none_or(|high| version < high);
        above_low && below_high
    }
}

fn parse_comparator(token: &str) -> Option<Bound> {
    let token = token.trim();
    if token.is_empty() || token == "*" || token.eq_ignore_ascii_case("x") {
        return Some(Bound {
            low: None,
            low_inclusive: true,
            high: None,
        });
    }
    let (operator, rest) = if let Some(rest) = token.strip_prefix(">=") {
        (">=", rest)
    } else if let Some(rest) = token.strip_prefix("<=") {
        ("<=", rest)
    } else if let Some(rest) = token.strip_prefix('>') {
        (">", rest)
    } else if let Some(rest) = token.strip_prefix('<') {
        ("<", rest)
    } else if let Some(rest) = token.strip_prefix('^') {
        ("^", rest)
    } else if let Some(rest) = token.strip_prefix('~') {
        ("~", rest)
    } else {
        ("=", token.strip_prefix('=').unwrap_or(token))
    };
    let version = parse_version(rest)?;
    let precision = version_precision(rest);
    Some(match operator {
        ">=" => Bound {
            low: Some(version),
            low_inclusive: true,
            high: None,
        },
        // `>22` excludes all of 22.x, not just 22.0.0, so a partial version
        // advances to the start of the next release line.
        ">" if precision < 3 => Bound {
            low: Some(next_at(version, precision)),
            low_inclusive: true,
            high: None,
        },
        ">" => Bound {
            low: Some(version),
            low_inclusive: false,
            high: None,
        },
        "<=" => Bound {
            low: None,
            low_inclusive: true,
            high: Some(next_at(version, precision)),
        },
        "<" => Bound {
            low: None,
            low_inclusive: true,
            high: Some(version),
        },
        "^" => Bound {
            low: Some(version),
            low_inclusive: true,
            high: Some(if version.0[0] > 0 {
                NodeVersion([version.0[0] + 1, 0, 0])
            } else {
                NodeVersion([0, version.0[1] + 1, 0])
            }),
        },
        "~" => Bound {
            low: Some(version),
            low_inclusive: true,
            high: Some(if precision >= 2 {
                NodeVersion([version.0[0], version.0[1] + 1, 0])
            } else {
                NodeVersion([version.0[0] + 1, 0, 0])
            }),
        },
        _ => Bound {
            low: Some(version),
            low_inclusive: true,
            high: Some(next_at(version, precision)),
        },
    })
}

/// Evaluates an `engines.node` range against a Node.js version.
pub fn supports(range: &str, node_version: &str) -> EngineSupport {
    let range = range.trim();
    if range.is_empty() || range == "*" {
        return EngineSupport::Satisfied;
    }
    let Some(version) = parse_version(node_version) else {
        return EngineSupport::Unknown;
    };
    // Hyphen ranges ("18 - 20") are not handled; treating them as unknown keeps
    // the install unblocked rather than guessing at the wrong interval.
    if range.split_whitespace().any(|token| token == "-") {
        return EngineSupport::Unknown;
    }
    let mut satisfied = false;
    for alternative in range.split("||") {
        let mut bounds = Vec::new();
        for token in alternative.split_whitespace() {
            let Some(bound) = parse_comparator(token) else {
                return EngineSupport::Unknown;
            };
            bounds.push(bound);
        }
        if bounds.is_empty() {
            continue;
        }
        if bounds.iter().all(|bound| bound.admits(version)) {
            satisfied = true;
        }
    }
    if satisfied {
        EngineSupport::Satisfied
    } else {
        EngineSupport::Unsatisfied
    }
}

/// One `EBADENGINE` block from npm's output.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct EngineMismatch {
    /// The offending package, as `name@version`.
    pub package: String,
    pub required: String,
    pub current: String,
}

impl EngineMismatch {
    /// npm reports mismatches for transitive dependencies too, and those are
    /// advisory: only a mismatch on the package being installed means the tool
    /// the user asked for will not run.
    pub fn is_package(&self, package_name: &str) -> bool {
        self.package
            .rsplit_once('@')
            .map(|(name, _)| name)
            .filter(|name| !name.is_empty())
            .unwrap_or(&self.package)
            == package_name
    }
}

fn quoted_after(line: &str, key: &str) -> Option<String> {
    let rest = line.split_once(key)?.1;
    let start = rest.find('\'')? + 1;
    let end = rest[start..].find('\'')? + start;
    Some(rest[start..end].to_string())
}

/// Extracts engine mismatches from npm's output.
///
/// npm emits these as warnings and still exits 0, so the text is the only
/// signal that an install produced something the current Node.js cannot run.
/// The prefix is `npm warn` on npm 10+ and `npm WARN` before that.
pub fn parse_engine_mismatches(output: &str) -> Vec<EngineMismatch> {
    let mut mismatches = Vec::new();
    let mut package: Option<String> = None;
    let mut required: Option<String> = None;
    let mut current: Option<String> = None;
    for line in output.lines() {
        let Some(body) = engine_warning_body(line) else {
            continue;
        };
        if body.starts_with("package:") {
            package = quoted_after(body, "package:");
        } else if body.starts_with("required:") {
            required = quoted_after(body, "node:");
        } else if body.starts_with("current:") {
            current = quoted_after(body, "node:");
        }
        if body.starts_with('}') {
            if let (Some(package), Some(required), Some(current)) =
                (package.take(), required.take(), current.take())
            {
                mismatches.push(EngineMismatch {
                    package,
                    required,
                    current,
                });
            }
            (package, required, current) = (None, None, None);
        }
    }
    mismatches
}

fn engine_warning_body(line: &str) -> Option<&str> {
    let trimmed = line.trim();
    let rest = trimmed.strip_prefix("npm")?.trim_start();
    let rest = rest
        .strip_prefix("warn")
        .or_else(|| rest.strip_prefix("WARN"))?
        .trim_start();
    Some(rest.strip_prefix("EBADENGINE")?.trim_start())
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The range openclaw ships, against the Node.js release that triggered the
    /// user report. 24.13.0 sits in the gap between two alternatives, so any
    /// check that only compares against the lowest accepted version passes it.
    #[test]
    fn disjoint_alternatives_reject_versions_that_fall_between_them() {
        let range = ">=22.22.3 <23 || >=24.15.0 <25 || >=25.9.0";
        assert_eq!(supports(range, "v24.13.0"), EngineSupport::Unsatisfied);
        assert_eq!(supports(range, "v22.22.3"), EngineSupport::Satisfied);
        assert_eq!(supports(range, "v22.30.0"), EngineSupport::Satisfied);
        assert_eq!(supports(range, "v24.15.0"), EngineSupport::Satisfied);
        assert_eq!(supports(range, "v25.9.1"), EngineSupport::Satisfied);
        assert_eq!(supports(range, "v23.1.0"), EngineSupport::Unsatisfied);
        assert_eq!(supports(range, "v22.22.2"), EngineSupport::Unsatisfied);
        assert_eq!(supports(range, "v25.8.9"), EngineSupport::Unsatisfied);
    }

    #[test]
    fn partial_versions_cover_their_whole_release_line() {
        assert_eq!(supports(">=18", "v18.0.0"), EngineSupport::Satisfied);
        assert_eq!(supports("<23", "v22.99.99"), EngineSupport::Satisfied);
        assert_eq!(supports("<23", "v23.0.0"), EngineSupport::Unsatisfied);
        assert_eq!(supports("22", "v22.5.1"), EngineSupport::Satisfied);
        assert_eq!(supports("22", "v23.0.0"), EngineSupport::Unsatisfied);
        assert_eq!(supports("22.5", "v22.5.9"), EngineSupport::Satisfied);
        assert_eq!(supports("22.5", "v22.6.0"), EngineSupport::Unsatisfied);
        assert_eq!(supports(">22", "v23.0.0"), EngineSupport::Satisfied);
        assert_eq!(supports(">22", "v22.9.9"), EngineSupport::Unsatisfied);
    }

    #[test]
    fn caret_and_tilde_bound_the_expected_range() {
        assert_eq!(supports("^20.1.0", "v20.9.9"), EngineSupport::Satisfied);
        assert_eq!(supports("^20.1.0", "v21.0.0"), EngineSupport::Unsatisfied);
        assert_eq!(supports("^20.1.0", "v20.0.9"), EngineSupport::Unsatisfied);
        assert_eq!(supports("~20.1.0", "v20.1.9"), EngineSupport::Satisfied);
        assert_eq!(supports("~20.1.0", "v20.2.0"), EngineSupport::Unsatisfied);
    }

    #[test]
    fn inclusive_upper_bounds_admit_the_named_release() {
        assert_eq!(supports("<=20.1.0", "v20.1.0"), EngineSupport::Satisfied);
        assert_eq!(supports("<=20.1.0", "v20.1.1"), EngineSupport::Unsatisfied);
        assert_eq!(supports("<=20", "v20.99.0"), EngineSupport::Satisfied);
    }

    /// Anything this module cannot interpret must not block an install, since
    /// npm itself would only have warned.
    #[test]
    fn unparseable_input_is_unknown_rather_than_unsatisfied() {
        assert_eq!(supports("18 - 20", "v19.0.0"), EngineSupport::Unknown);
        assert_eq!(supports(">=nonsense", "v20.0.0"), EngineSupport::Unknown);
        assert_eq!(supports(">=18", "not-a-version"), EngineSupport::Unknown);
    }

    #[test]
    fn an_absent_requirement_is_always_satisfied() {
        assert_eq!(supports("", "v20.0.0"), EngineSupport::Satisfied);
        assert_eq!(supports("*", "v20.0.0"), EngineSupport::Satisfied);
    }

    /// Verbatim from the user report that prompted this check.
    const OPENCLAW_WARNING: &str = "npm warn EBADENGINE Unsupported engine {
npm warn EBADENGINE   package: 'openclaw@2026.7.1-2',
npm warn EBADENGINE   required: { node: '>=22.22.3 <23 || >=24.15.0 <25 || >=25.9.0' },
npm warn EBADENGINE   current: { node: 'v24.13.0', npm: '11.19.0' }
npm warn EBADENGINE }";

    #[test]
    fn an_engine_warning_is_parsed_into_its_three_facts() {
        let mismatches = parse_engine_mismatches(OPENCLAW_WARNING);
        assert_eq!(
            mismatches,
            vec![EngineMismatch {
                package: "openclaw@2026.7.1-2".to_string(),
                required: ">=22.22.3 <23 || >=24.15.0 <25 || >=25.9.0".to_string(),
                current: "v24.13.0".to_string(),
            }]
        );
        // The parsed pieces have to agree with the evaluator, or the app would
        // report a mismatch it cannot explain.
        let mismatch = &mismatches[0];
        assert_eq!(
            supports(&mismatch.required, &mismatch.current),
            EngineSupport::Unsatisfied
        );
    }

    #[test]
    fn mismatches_are_attributed_to_the_right_package() {
        let mismatches = parse_engine_mismatches(OPENCLAW_WARNING);
        assert!(mismatches[0].is_package("openclaw"));
        assert!(!mismatches[0].is_package("codex"));
    }

    #[test]
    fn scoped_package_names_survive_attribution() {
        let mismatch = EngineMismatch {
            package: "@openai/codex@1.2.3".to_string(),
            required: ">=22".to_string(),
            current: "v20.0.0".to_string(),
        };
        assert!(mismatch.is_package("@openai/codex"));
        assert!(!mismatch.is_package("codex"));
    }

    #[test]
    fn the_legacy_uppercase_warning_prefix_is_understood() {
        let output = "npm WARN EBADENGINE Unsupported engine {
npm WARN EBADENGINE   package: 'legacy@1.0.0',
npm WARN EBADENGINE   required: { node: '>=18' },
npm WARN EBADENGINE   current: { node: 'v16.0.0', npm: '8.0.0' }
npm WARN EBADENGINE }";
        let mismatches = parse_engine_mismatches(output);
        assert_eq!(mismatches.len(), 1);
        assert_eq!(mismatches[0].required, ">=18");
    }

    #[test]
    fn several_blocks_are_kept_apart() {
        let output = format!("{OPENCLAW_WARNING}\n{OPENCLAW_WARNING}");
        assert_eq!(parse_engine_mismatches(&output).len(), 2);
    }

    #[test]
    fn output_without_engine_warnings_yields_nothing() {
        assert!(parse_engine_mismatches("added 1 package in 2s").is_empty());
        assert!(parse_engine_mismatches("npm warn deprecated foo@1.0.0").is_empty());
    }
}
