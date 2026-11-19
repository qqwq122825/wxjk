#pragma once

#include <cmath>
#include <cstdint>
#include <map>
#include <string>

namespace jk {

enum class Decision { accepted, duplicate, invalid, stale, conflict, full };

// All times are monotonic seconds. Browser presence and network connectivity
// deliberately have no influence on a phone-owned task.
class CapturePolicy {
public:
    struct Record { uint64_t version; bool terminal; };
    std::map<std::string, Record> records;
    std::string taskID;
    uint64_t version = 0;
    double interval = 3;
    double deadline = 0; // zero means until explicitly stopped
    double nextCapture = 0;
    bool desiredRunning = false;
    bool active = false;
    uint64_t generation = 0;

    Decision start(const std::string &id, uint64_t revision, double seconds,
                   double expires, double now) {
        if (id.empty() || id.size() > 200 || revision == 0 ||
            !std::isfinite(seconds) || seconds < 0.5 || seconds > 3600 ||
            !std::isfinite(expires) || !std::isfinite(now) ||
            (expires != 0 && expires <= now)) return Decision::invalid;
        expire(now);
        auto found = records.find(id);
        if (found != records.end()) {
            if (found->second.terminal || revision < found->second.version)
                return Decision::stale;
            if (taskID == id && revision == version && desiredRunning)
                return Decision::duplicate;
            return Decision::conflict;
        }
        if (desiredRunning) return Decision::conflict;
        if (records.size() >= 1024) return Decision::full;
        records[id] = {revision, false};
        taskID = id;
        version = revision;
        interval = seconds;
        deadline = expires;
        nextCapture = now;
        desiredRunning = true;
        ++generation;
        return Decision::accepted;
    }

    Decision stop(const std::string &id, uint64_t revision) {
        if (id.empty() || id.size() > 200 || revision == 0) return Decision::invalid;
        auto found = records.find(id);
        if (found != records.end()) {
            if (revision < found->second.version) return Decision::stale;
            if (revision == found->second.version)
                return found->second.terminal ? Decision::duplicate : Decision::stale;
        } else if (records.size() >= 1024) return Decision::full;
        records[id] = {revision, true};
        if (taskID == id) {
            version = revision;
            desiredRunning = false;
            ++generation;
        }
        return Decision::accepted;
    }

    bool expire(double now) {
        if (!desiredRunning || deadline == 0 || now < deadline) return false;
        desiredRunning = false;
        records[taskID] = {version, true};
        ++generation;
        return true;
    }

    void setActive(bool value, double now) {
        if (active != value) {
            active = value;
            nextCapture = now; // resume once, never replay missed timer ticks
            ++generation;
        }
    }

    bool due(double now) {
        expire(now);
        return desiredRunning && active && now >= nextCapture;
    }

    void attempted(double now) { nextCapture = now + interval; }
    bool valid(uint64_t ticket, double now) {
        expire(now);
        return desiredRunning && active && ticket == generation;
    }
};
} // namespace jk
