#include "JKCapturePolicy.hpp"
#include "JKReconnectPolicy.hpp"
#include <cstdlib>
#include <iostream>
#include <limits>

static int checks = 0;
#define CHECK(condition) do { ++checks; if (!(condition)) { \
    std::cerr << "FAILED line " << __LINE__ << ": " #condition << '\n'; std::exit(1); \
} } while (0)

int main() {
    using jk::CapturePolicy;
    using jk::Decision;
    {
        CapturePolicy p;
        CHECK(p.start("phone-owned", 1, 3, 0, 10) == Decision::accepted);
        CHECK(!p.due(10));
        p.setActive(true, 10);
        CHECK(p.due(10));
        p.attempted(10);
        CHECK(!p.due(12.9));
        CHECK(p.due(13));
        auto ticket = p.generation;
        p.setActive(false, 14);
        CHECK(!p.due(100));
        CHECK(!p.valid(ticket, 100));
        CHECK(p.desiredRunning);
        p.setActive(true, 1000);
        CHECK(p.due(1000));
        p.attempted(1000);
        CHECK(!p.due(1001));
        CHECK(p.due(1003));
        // No browser renewal or network heartbeat is required for this task.
        CHECK(p.due(86400));
        CHECK(p.desiredRunning);
    }
    {
        CapturePolicy p;
        p.setActive(true, 0);
        CHECK(p.start("deadline", 1, 3, 10, 0) == Decision::accepted);
        p.attempted(0);
        CHECK(p.start("deadline", 1, 99, 100, 2) == Decision::duplicate);
        CHECK(p.interval == 3 && p.deadline == 10 && p.nextCapture == 3);
        auto ticket = p.generation;
        CHECK(p.valid(ticket, 9));
        CHECK(!p.valid(ticket, 10));
        CHECK(!p.desiredRunning);
        p.setActive(false, 11);
        p.setActive(true, 20);
        CHECK(!p.due(20));
        CHECK(p.start("deadline", 2, 3, 0, 20) == Decision::stale);
    }
    {
        CapturePolicy p;
        p.setActive(true, 0);
        CHECK(p.start("a", 2, 3, 0, 0) == Decision::accepted);
        auto ticket = p.generation;
        CHECK(p.stop("a", 1) == Decision::stale);
        CHECK(p.stop("a", 2) == Decision::stale);
        CHECK(p.valid(ticket, 1));
        CHECK(p.stop("a", 3) == Decision::accepted);
        CHECK(!p.valid(ticket, 1));
        CHECK(p.stop("a", 3) == Decision::duplicate);
        CHECK(p.start("a", 4, 3, 0, 2) == Decision::stale);
        CHECK(p.start("b", 1, 3, 0, 2) == Decision::accepted);
        CHECK(p.stop("a", 4) == Decision::accepted);
        CHECK(p.desiredRunning && p.taskID == "b");
        CHECK(p.start("c", 1, 3, 0, 2) == Decision::conflict);
    }
    {
        CapturePolicy p;
        CHECK(p.stop("arrived-first", 2) == Decision::accepted);
        CHECK(p.start("arrived-first", 1, 3, 0, 0) == Decision::stale);
        CHECK(p.start("arrived-first", 3, 3, 0, 0) == Decision::stale);
        CHECK(!p.desiredRunning);
    }
    {
        CapturePolicy p;
        const double nan = std::numeric_limits<double>::quiet_NaN();
        const double inf = std::numeric_limits<double>::infinity();
        CHECK(p.start("", 1, 3, 0, 0) == Decision::invalid);
        CHECK(p.start("x", 0, 3, 0, 0) == Decision::invalid);
        CHECK(p.start("x", 1, 2, 0, 0) == Decision::invalid);
        CHECK(p.start("x", 1, nan, 0, 0) == Decision::invalid);
        CHECK(p.start("x", 1, inf, 0, 0) == Decision::invalid);
        CHECK(p.start("x", 1, 3, inf, 0) == Decision::invalid);
        CHECK(p.start("x", 1, 3, 5, 5) == Decision::invalid);
        CHECK(p.start("x", 1, 3, 0, nan) == Decision::invalid);
        CHECK(p.stop("", 1) == Decision::invalid);
        CHECK(p.records.empty());
    }
    {
        jk::ReconnectPolicy retry;
        CHECK(retry.nextDelay(1) == 1);
        CHECK(retry.nextDelay(1) == 2);
        CHECK(retry.nextDelay(1) == 4);
        CHECK(retry.nextDelay(1) == 8);
        CHECK(retry.nextDelay(1) == 16);
        CHECK(retry.nextDelay(1) == 32);
        CHECK(retry.nextDelay(1) == 60);
        CHECK(retry.nextDelay(1) == 60);
        CHECK(retry.nextDelay(0) == 0.5);
        CHECK(retry.nextDelay(0.5) >= 0.5 && retry.failures > 0);
        retry.synchronized();
        CHECK(retry.failures == 0 && retry.nextDelay(1) == 1);
    }
    std::cout << "Capture / reconnect policy: " << checks << " checks passed\n";
}
