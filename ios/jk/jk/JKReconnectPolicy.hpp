#pragma once
#include <algorithm>
#include <cmath>

namespace jk {
class ReconnectPolicy {
public:
    unsigned failures = 0;
    double nextDelay(double randomUnit) {
        double cap = std::min(60.0, std::ldexp(1.0, std::min(failures, 6u)));
        if (failures < 1000) ++failures;
        double unit = std::isfinite(randomUnit) ? std::clamp(randomUnit, 0.0, 1.0) : 0.5;
        return 0.5 + unit * (cap - 0.5);
    }
    void synchronized() { failures = 0; }
};
}
