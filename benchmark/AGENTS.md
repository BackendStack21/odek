# Benchmark Project Instructions

You are running inside an automated benchmark. Follow these rules strictly:

## Output File Paths
- When the task says "Write to benchmark_data/output/X.py", use write_file with path "benchmark_data/output/X.py" — exactly as specified.
- Never drop directories. "benchmark_data/output/X.py" must be written as "benchmark_data/output/X.py", not "X.py".
- Use write_file for all new files. Do NOT use patch or shell commands to create files.

## Source Files — READ ONLY
- Files in benchmark_data/ are source files. Read them with read_file but NEVER modify them.
- If the task asks you to refactor or write tests, create NEW files in benchmark_data/output/.
- Never write to benchmark_data/refactor_me.py, benchmark_data/under_tested.py, or any file already in benchmark_data/.

## Follow the Design Spec
- If the task specifies a function signature like `validate_user(data, rules)`, implement it EXACTLY as described.
- Do NOT create a different function signature, a generic framework, or a different design.
- If validators are specified (is_string, is_email, is_int), define them as named functions matching exactly.

## One Write is Enough
- Write each output file ONCE. Do not write, then test, then rewrite.
- After writing a file, do NOT run tests, pytest, or any verification unless the task explicitly asks.
- If you make a mistake, correct it with patch — do not rewrite the entire file.

## Tasks
### Task 3.2 (add_test)
Write tests to `benchmark_data/output/test_under_tested.py`. Use stdlib unittest. 
Run with: `python3 benchmark_data/output/test_under_tested.py`

### Task 3.3 (refactor)
Write refactored code to `benchmark_data/output/refactored.py`.
Define: `def validate_user(data, rules)` where rules is a dict of validator functions.
Validators: is_string, is_email, is_int, is_positive_int, is_one_of.
Keep the original imports. Keep `def format_user`.
