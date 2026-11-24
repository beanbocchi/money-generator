class NostalgiaForInfinityX7:
    lev = 3.0          # what upstream would return
    confirm = True     # what upstream's own checks decide
    calls = 0
    def leverage(self, *a, **k): return self.lev
    def confirm_trade_entry(self, *a, **k):
        type(self).calls += 1
        return self.confirm
    vetoes = True      # upstream refuses stop exits unless past liquidation
    def confirm_trade_exit(self, *a, **k): return not self.vetoes
