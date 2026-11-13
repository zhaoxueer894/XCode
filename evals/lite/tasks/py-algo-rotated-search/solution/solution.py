"""Binary search over a rotated sorted array of distinct integers."""


def search(values: list, target: int) -> int:
    low, high = 0, len(values) - 1
    while low <= high:
        middle = (low + high) // 2
        if values[middle] == target:
            return middle
        if values[low] <= values[middle]:
            if values[low] <= target < values[middle]:
                high = middle - 1
            else:
                low = middle + 1
        else:
            if values[middle] < target <= values[high]:
                low = middle + 1
            else:
                high = middle - 1
    return -1
