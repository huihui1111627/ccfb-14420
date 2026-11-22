package rtree

func heapPush(h *nearHeap, x nearItem) {
	*h = append(*h, x)
	i := len(*h) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if !h.Less(i, parent) {
			break
		}
		h.Swap(i, parent)
		i = parent
	}
}

func heapPop(h *nearHeap) nearItem {
	old := *h
	n := len(old)
	top := old[0]
	old[0] = old[n-1]
	*h = old[:n-1]
	i := 0
	for {
		l, r := 2*i+1, 2*i+2
		small := i
		if l < len(*h) && h.Less(l, small) {
			small = l
		}
		if r < len(*h) && h.Less(r, small) {
			small = r
		}
		if small == i {
			break
		}
		h.Swap(i, small)
		i = small
	}
	return top
}
