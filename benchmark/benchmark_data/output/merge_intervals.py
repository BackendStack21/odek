def merge_intervals(intervals):
    """Merge overlapping intervals.

    Takes a list of [start, end] integer pairs and returns a new list of
    non-overlapping intervals sorted by start value. Overlapping adjacent
    intervals (end of one >= start of next) are merged.

    Args:
        intervals: List of [start, end] integer pairs. May be empty,
                   unsorted, contain negatives, or have any overlap pattern.

    Returns:
        A new list of merged [start, end] intervals sorted by start.

    Examples:
        >>> merge_intervals([])
        []
        >>> merge_intervals([[1, 3]])
        [[1, 3]]
        >>> merge_intervals([[1, 3], [2, 6], [8, 10]])
        [[1, 6], [8, 10]]
        >>> merge_intervals([[1, 4], [4, 5]])
        [[1, 5]]
        >>> merge_intervals([[1, 4], [2, 3]])
        [[1, 4]]
        >>> merge_intervals([[7, 10], [1, 2], [3, 5]])
        [[1, 2], [3, 5], [7, 10]]
        >>> merge_intervals([[-5, -1], [-3, 0], [1, 2]])
        [[-5, 0], [1, 2]]
    """
    if not intervals:
        return []

    # Sort by start value
    sorted_intervals = sorted(intervals, key=lambda x: x[0])

    merged = [sorted_intervals[0]]

    for current in sorted_intervals[1:]:
        prev = merged[-1]

        if current[0] <= prev[1]:  # overlap or adjacent
            # Merge: keep previous start, extend end if current ends later
            merged[-1] = [prev[0], max(prev[1], current[1])]
        else:
            merged.append(current)

    return merged
